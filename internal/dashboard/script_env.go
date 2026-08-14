package dashboard

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// GET /api/scripts/env — reports the HOST execution facts for a bash step, so
// the editor can show an accurate "this runs on your machine" callout with the
// CLIs actually available. Bash steps run in the dashboard's host process
// (os.Environ()), so "available" = on the dashboard's PATH, and "authenticated"
// = a cheap auth probe succeeded for the CLIs that have one.
//
// Probes are best-effort with short timeouts; a missing/slow CLI is simply
// reported as unavailable. Nothing is mutated.

type cliStatus struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Authenticated *bool  `json:"authenticated,omitempty"` // nil = no auth concept / not probed
	Detail        string `json:"detail,omitempty"`
}

// authProbe is an optional command that reports whether a CLI is logged in
// (exit 0 = authenticated). Kept read-only + fast.
type cliProbe struct {
	name  string
	auth  []string // "" → no auth probe (presence only)
	label string
}

var scriptCLIs = []cliProbe{
	{name: "gh", auth: []string{"auth", "status"}, label: "GitHub CLI"},
	{name: "git", label: "git"},
	{name: "aws", auth: []string{"sts", "get-caller-identity"}, label: "AWS CLI"},
	{name: "gcloud", auth: []string{"auth", "list", "--filter=status:ACTIVE", "--format=value(account)"}, label: "Google Cloud"},
	{name: "kubectl", label: "kubectl"},
	{name: "docker", auth: []string{"info"}, label: "Docker"},
	{name: "curl", label: "curl"},
	{name: "jq", label: "jq"},
	{name: "slack", label: "Slack CLI"},
}

func (d *dashboardServer) handleScriptEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out := make([]cliStatus, 0, len(scriptCLIs))
	for _, p := range scriptCLIs {
		st := cliStatus{Name: p.name, Detail: p.label}
		if _, err := exec.LookPath(p.name); err != nil {
			out = append(out, st) // not available
			continue
		}
		st.Available = true

		if len(p.auth) > 0 {
			authed := probeAuth(p.name, p.auth)
			st.Authenticated = &authed
		}
		out = append(out, st)
	}

	writeJSON(w, map[string]any{
		// The facts the callout states — kept here so the copy stays truthful.
		"host":    true,
		"note":    "Scripts run on the machine hosting this dashboard, in its shell environment, with any CLIs already signed in there. There is no sandbox.",
		"clis":    out,
	})
}

// probeAuth runs a fast, read-only auth check and returns whether it succeeded.
func probeAuth(name string, args []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	// gcloud's active-account probe exits 0 even with no account; treat empty
	// output as unauthenticated.
	if name == "gcloud" && strings.TrimSpace(string(out)) == "" {
		return false
	}
	return true
}
