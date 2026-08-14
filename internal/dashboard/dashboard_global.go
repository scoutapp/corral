package dashboard

import (
	"encoding/json"
	"fmt"
	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/creds"
	"github.com/scoutapp/corral/internal/session"
	sshagent "github.com/scoutapp/corral/internal/ssh"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ----------------------------------------------------------------------------
// Global (cross-project) control plane.
//
// Two things live at the host level, above any single project:
//   - Shared credentials: ~/.corral/proxy-credentials.json, merged into every
//     project's proxy (project overrides win per-domain). Editing here reloads the
//     mitmweb of every running project (each watches its creds file via mtime).
//   - Defaults: monitor-list + mitm-ports that NEW projects inherit at init.
//     Stored in ~/.corral/defaults.json. Existing projects are untouched.
//
// Credential values are shown partially masked (tail revealed) so you can tell
// keys apart without exposing the secret — see maskTail.
// ----------------------------------------------------------------------------

type GlobalDefaults struct {
	MonitorHosts []string `json:"monitor_hosts,omitempty"`
	MitmPorts    []string `json:"mitm_ports,omitempty"`
}

func globalDefaultsPath() string {
	return filepath.Join(config.CorralHome(), "defaults.json")
}

func ReadGlobalDefaults() GlobalDefaults {
	var d GlobalDefaults
	data, err := os.ReadFile(globalDefaultsPath())
	if err != nil {
		return d
	}
	json.Unmarshal(data, &d)
	return d
}

func writeGlobalDefaults(d GlobalDefaults) error {
	if err := os.MkdirAll(config.CorralHome(), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(globalDefaultsPath(), data, 0600)
}

// maskTail reveals only the last 4 characters of a secret, starring the rest:
// "sk-ant-abcd…wxyz" -> "••••••••wxyz". Lets you identify which key is set
// without exposing it. Short values are fully masked.
func maskTail(value string) string {
	const reveal = 4
	if len(value) <= reveal {
		return strings.Repeat("•", len(value))
	}
	// Cap the dots so a very long token doesn't produce a giant string.
	dots := len(value) - reveal
	if dots > 8 {
		dots = 8
	}
	return strings.Repeat("•", dots) + value[len(value)-reveal:]
}

type globalCredView struct {
	Host   string `json:"host"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Masked string `json:"masked"` // tail-revealed value
}

type globalView struct {
	Credentials  []globalCredView `json:"credentials"`
	MonitorHosts []string         `json:"monitor_hosts"`
	MitmPorts    []string         `json:"mitm_ports"`
	CredsPath    string           `json:"creds_path"`

	// SSH default key set (~/.corral/ssh-keys.json) — the always-on base for
	// every project. AvailableSSHKeys lets the picker render checkboxes.
	SSHKeys         []string                `json:"ssh_keys"`
	SSHKeysPath     string                  `json:"ssh_keys_path"`
	AvailableSSHKey []sshagent.AvailableKey `json:"available_ssh_keys"`

	// UpdateRepo is the GitHub owner/name self-updates are pulled from. Blank
	// input in the UI means "use the default" — we surface the effective value and
	// the default so the field can show a placeholder.
	UpdateRepo        string `json:"update_repo"`
	UpdateRepoDefault string `json:"update_repo_default"`

	// Application-log retention (app_logs). Zero = built-in defaults, surfaced so
	// the UI can show them as placeholders.
	LogRetentionDays        int `json:"log_retention_days"`
	LogMaxRows              int `json:"log_max_rows"`
	LogRetentionDaysDefault int `json:"log_retention_days_default"`
	LogMaxRowsDefault       int `json:"log_max_rows_default"`

	// ApiWritesEnabled — whether the corral api CLI / host Claude skill may make
	// mutating calls. Off by default; surfaced so the UI can render the toggle.
	ApiWritesEnabled bool `json:"api_writes_enabled"`
}

func (d *dashboardServer) handleGlobalRead(w http.ResponseWriter, r *http.Request) {
	credsMap, _ := creds.LoadCredsMap(creds.GlobalCredentialsPath())
	view := globalView{CredsPath: creds.GlobalCredentialsPath()}

	for host, entry := range credsMap {
		cv := globalCredView{Host: host, Masked: maskTail(entry["value"])}
		if v, ok := entry["header"]; ok {
			cv.Kind, cv.Name = "header", v
		} else if v, ok := entry["url_param"]; ok {
			cv.Kind, cv.Name = "url_param", v
		}
		view.Credentials = append(view.Credentials, cv)
	}
	sort.Slice(view.Credentials, func(i, j int) bool { return view.Credentials[i].Host < view.Credentials[j].Host })

	def := ReadGlobalDefaults()
	view.MonitorHosts = def.MonitorHosts
	view.MitmPorts = def.MitmPorts
	if len(view.MitmPorts) == 0 {
		view.MitmPorts = []string{"80", "443"}
	}

	view.SSHKeys = config.GlobalSSHKeys()
	view.SSHKeysPath = config.GlobalSSHKeysPath()
	view.AvailableSSHKey = sshagent.AvailableKeys()

	// Show the raw configured value (empty = using the default) plus the default
	// so the UI can render it as a placeholder.
	gs := config.ReadGlobalSettings()
	view.UpdateRepo = gs.UpdateRepo
	view.UpdateRepoDefault = config.DefaultUpdateRepo
	view.LogRetentionDays = gs.LogRetentionDays
	view.LogMaxRows = gs.LogMaxRows
	view.LogRetentionDaysDefault = applog.DefaultRetention.MaxAgeDays
	view.LogMaxRowsDefault = applog.DefaultRetention.MaxRows
	view.ApiWritesEnabled = gs.ApiWritesEnabled

	writeJSON(w, view)
}

// handleGlobalApply writes global credential and default changes. Credential
// edits reload every running project's mitmweb (each watches its own creds file,
// and the global file is merged in). Defaults only affect future `corral
// init`, so no reload is needed for them.
func (d *dashboardServer) handleGlobalApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var edit struct {
		SetCreds         []credSet `json:"set_creds,omitempty"`
		UnsetCreds       []string  `json:"unset_creds,omitempty"`
		MonitorHosts     *[]string `json:"monitor_hosts,omitempty"`
		MitmPorts        *[]string `json:"mitm_ports,omitempty"`
		SSHKeys          *[]string `json:"ssh_keys,omitempty"`
		UpdateRepo       *string   `json:"update_repo,omitempty"`
		LogRetentionDays *int      `json:"log_retention_days,omitempty"`
		LogMaxRows       *int      `json:"log_max_rows,omitempty"`
		ApiWritesEnabled *bool     `json:"api_writes_enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
		http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	results := []string{}

	if len(edit.SetCreds) > 0 || len(edit.UnsetCreds) > 0 {
		credsMap, err := creds.LoadCredsMap(creds.GlobalCredentialsPath())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, c := range edit.SetCreds {
			if c.Kind != "header" && c.Kind != "url_param" {
				results = append(results, "✗ "+c.Host+": kind must be header|url_param")
				continue
			}
			credsMap[strings.ToLower(c.Host)] = map[string]string{c.Kind: c.Name, "value": c.Value}
			results = append(results, "✓ credential set: "+c.Host)
		}
		for _, h := range edit.UnsetCreds {
			delete(credsMap, strings.ToLower(h))
			results = append(results, "✓ credential removed: "+h)
		}
		if err := creds.WriteCredsMap(creds.GlobalCredentialsPath(), credsMap); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		n := reloadAllRunningProjectMitmweb()
		results = append(results, fmt.Sprintf("✓ %d running project(s) will pick up global creds within ~1s", n))
	}

	if edit.MonitorHosts != nil || edit.MitmPorts != nil {
		def := ReadGlobalDefaults()
		if edit.MonitorHosts != nil {
			def.MonitorHosts = *edit.MonitorHosts
		}
		if edit.MitmPorts != nil {
			def.MitmPorts = *edit.MitmPorts
		}
		if err := writeGlobalDefaults(def); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		results = append(results, "✓ defaults saved (new projects inherit these at init)")
	}

	if edit.SSHKeys != nil {
		if err := config.WriteGlobalSSHKeys(*edit.SSHKeys); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		results = append(results, fmt.Sprintf("✓ %d global ssh key(s) saved (loaded by every project on next start)", len(*edit.SSHKeys)))
	}

	if edit.UpdateRepo != nil {
		raw := strings.TrimSpace(*edit.UpdateRepo)
		// Empty clears the override (back to the default source). A non-empty value
		// must normalize to owner/name.
		if raw != "" {
			if _, ok := config.NormalizeRepo(raw); !ok {
				http.Error(w, fmt.Sprintf("%q doesn't look like a GitHub owner/name (e.g. %s) or a release URL (e.g. https://host/owner/repo)", raw, config.DefaultUpdateRepo), http.StatusBadRequest)
				return
			}
		}
		gs := config.ReadGlobalSettings()
		gs.UpdateRepo = raw
		if err := config.WriteGlobalSettings(gs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if raw == "" {
			results = append(results, "✓ update source reset to default ("+config.DefaultUpdateRepo+")")
		} else {
			results = append(results, "✓ update source set to "+gs.UpdateRepoOrDefault())
		}
	}

	if edit.LogRetentionDays != nil || edit.LogMaxRows != nil {
		gs := config.ReadGlobalSettings()
		if edit.LogRetentionDays != nil {
			gs.LogRetentionDays = *edit.LogRetentionDays // 0 = back to default
		}
		if edit.LogMaxRows != nil {
			gs.LogMaxRows = *edit.LogMaxRows
		}
		if err := config.WriteGlobalSettings(gs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		results = append(results, "✓ log retention updated")
	}

	if edit.ApiWritesEnabled != nil {
		gs := config.ReadGlobalSettings()
		gs.ApiWritesEnabled = *edit.ApiWritesEnabled
		if err := config.WriteGlobalSettings(gs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if *edit.ApiWritesEnabled {
			results = append(results, "✓ API writes enabled (the corral CLI / Claude can now make changes)")
		} else {
			results = append(results, "✓ API writes disabled (read-only for the CLI / Claude)")
		}
	}

	writeJSON(w, map[string]any{"results": results})
}

// reloadAllRunningProjectMitmweb touches nothing itself — the global creds file
// just changed, and every project's mitmweb addon watches its own credentials
// file. But a project reading ONLY the global file (no project override) will see
// the change; one merged at start may not. We report the count of running
// projects so the UI can set expectations. (Merge reconciliation is a known
// follow-up.) Returns how many projects are currently running.
func reloadAllRunningProjectMitmweb() int {
	reg, err := readRegistry()
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range reg.Projects {
		if session.DockerContainerRunning(session.ContainerNameForWorkspace(p.Workspace)) {
			n++
		}
	}
	return n
}

// handleGlobalPopulate spawns `corral populate-proxy-credentials` in a
// dedicated tmux session so the interactive `claude setup-token` flow (stdin +
// browser auth) can run, and returns the id of a browser terminal the dashboard
// can attach to. The user completes the prompts in that terminal pane.
func (d *dashboardServer) handleGlobalPopulate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	session := "corral-populate-creds"
	// Kill any prior populate session so a retry starts clean.
	exec.Command("tmux", "kill-session", "-t", session).Run()

	// Start the CLI in a detached tmux session. `; exec bash` keeps the pane open
	// after the command finishes so the user can read the result before closing.
	cmdline := fmt.Sprintf("%s populate-proxy-credentials; echo; echo '[done — you can close this]'; exec bash", exe)
	start := exec.Command("tmux", "new-session", "-d", "-s", session, "bash", "-lc", cmdline)
	if err := start.Run(); err != nil {
		http.Error(w, "failed to start populate session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"session": session})
}
