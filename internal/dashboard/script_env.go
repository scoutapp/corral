package dashboard

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// GET /api/scripts/env — reports the HOST execution facts for a bash step, so
// the editor can show an accurate callout with the CLIs actually available.
//
// Detection runs through the operator's LOGIN shell (`$SHELL -lc`), exactly how
// the bash steps themselves run — so the PATH matches the operator's terminal
// (aws, brew/nvm tools resolve) and the panel reflects what a script will really
// see. Only INSTALLED CLIs are returned; each carries a status:
//
//	"authed"     signed in (a read-only auth probe passed)
//	"unauthed"   installed but not signed in
//	"no_auth"    no authentication concept (curl, jq, git, …)
//
// Probes are best-effort with short timeouts; nothing is mutated.

const (
	cliAuthed   = "authed"
	cliUnauthed = "unauthed"
	cliNoAuth   = "no_auth"
)

type cliStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // authed | unauthed | no_auth
	Label  string `json:"label,omitempty"`
}

// cliProbe describes a CLI to detect. auth is a read-only login check (exit 0 =
// signed in); empty auth means the tool has no auth concept.
type cliProbe struct {
	name  string
	auth  []string
	label string
}

var scriptCLIs = []cliProbe{
	{name: "gh", auth: []string{"auth", "status"}, label: "GitHub CLI"},
	{name: "aws", auth: []string{"sts", "get-caller-identity"}, label: "AWS CLI"},
	{name: "gcloud", auth: []string{"auth", "list", "--filter=status:ACTIVE", "--format=value(account)"}, label: "Google Cloud"},
	{name: "git", label: "git"},
	{name: "kubectl", label: "kubectl"},
	// docker intentionally has NO auth probe: `docker info` tests the daemon, not
	// registry login, so a checkmark there would mislead. Presence only.
	{name: "docker", label: "Docker"},
	{name: "curl", label: "curl"},
	{name: "jq", label: "jq"},
	{name: "slack", label: "Slack CLI"},
}

func (d *dashboardServer) handleScriptEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	shell := loginShell()

	out := make([]cliStatus, 0, len(scriptCLIs))
	for _, p := range scriptCLIs {
		if !onLoginPath(shell, p.name) {
			continue // not installed / not on the login-shell PATH → omit
		}
		st := cliStatus{Name: p.name, Label: p.label, Status: cliNoAuth}
		if len(p.auth) > 0 {
			if probeAuthLogin(shell, p.name, p.auth) {
				st.Status = cliAuthed
			} else {
				st.Status = cliUnauthed
			}
		}
		out = append(out, st)
	}

	writeJSON(w, map[string]any{
		"host": true,
		"note": "Scripts run on the machine hosting this dashboard, in your login shell — your PATH and any CLIs you're already signed in to. There is no sandbox.",
		"clis": out,
	})
}

// onLoginPath reports whether `name` resolves on the login shell's PATH (the
// same PATH a bash step gets), via `command -v` inside `$SHELL -lc`.
func onLoginPath(shell, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// command -v is a shell builtin; quoting name keeps it a single arg.
	cmd := exec.CommandContext(ctx, shell, "-lc", "command -v "+shellQuote(name))
	return cmd.Run() == nil
}

// probeAuthLogin runs a read-only auth check for `name` through the login shell,
// returning whether it succeeded (signed in).
func probeAuthLogin(shell, name string, args []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	line := shellQuote(name)
	for _, a := range args {
		line += " " + shellQuote(a)
	}
	cmd := exec.CommandContext(ctx, shell, "-lc", line)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	// gcloud's active-account query exits 0 even with no account; empty output
	// means unauthenticated.
	if name == "gcloud" && strings.TrimSpace(string(out)) == "" {
		return false
	}
	return true
}

// shellQuote single-quotes a token for safe interpolation into a shell -lc line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
