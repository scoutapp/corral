package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/creds"
)

// Script secrets endpoints: GET/PUT /api/actions/<id>/secrets.
//
// GET returns, for a bash action, the env vars it appears to need (detected from
// the script) merged with any already-set secrets — VALUES MASKED, never sent to
// the browser (mirrors the cred UI). PUT stores values in the Keychain-backed
// script-secrets store and invalidates the redactor so new secrets are stripped
// from host-claude transcripts immediately.

type scriptSecretView struct {
	Name   string `json:"name"`             // env var name, e.g. FRESHDESK_API_KEY
	Set    bool   `json:"set"`              // whether a value is stored
	Masked string `json:"masked,omitempty"` // tail-revealed hint when set; never the full value
}

func (d *dashboardServer) handleActionSecrets(w http.ResponseWriter, r *http.Request, svc *automations.Service, idStr string) {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad action id", http.StatusBadRequest)
		return
	}
	act, err := svc.Action(id)
	if err != nil {
		http.Error(w, "action not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"secrets": d.scriptSecretsView(id, act)})

	case http.MethodPut:
		// Body: {"secrets":[{"name":"FRESHDESK_API_KEY","value":"..."}], "remove":["OLD"]}.
		// A value of "" for a present name leaves it unchanged (so the UI can submit
		// masked fields untouched); use "remove" to delete.
		var body struct {
			Secrets []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"secrets"`
			Remove []string `json:"remove"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		m, err := creds.LoadScriptSecrets(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, s := range body.Secrets {
			if s.Name == "" {
				continue
			}
			entry := m[s.Name]
			if entry == nil {
				entry = map[string]string{"kind": "env", "name": s.Name}
			}
			if s.Value != "" { // blank = keep existing (masked, untouched)
				entry["value"] = s.Value
			}
			m[s.Name] = entry
		}
		for _, name := range body.Remove {
			delete(m, name)
		}
		if err := creds.WriteScriptSecrets(id, m); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// New/changed secrets must be stripped from host-claude transcripts now.
		globalRedactor.invalidate()
		writeJSON(w, map[string]any{"secrets": d.scriptSecretsView(id, act)})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// scriptSecretsView merges detected-but-unset env vars with stored (masked)
// secrets for the action's bash script. Non-bash actions get an empty list.
func (d *dashboardServer) scriptSecretsView(id int64, act automations.Action) []scriptSecretView {
	stored, _ := creds.LoadScriptSecrets(id)

	// Detected vars from the script body (bash only).
	detected := map[string]bool{}
	if act.Kind == "bash" {
		var spec struct {
			Script string `json:"script"`
		}
		_ = json.Unmarshal([]byte(act.Spec), &spec)
		for _, name := range automations.DetectInjectableVars(spec.Script) {
			detected[name] = true
		}
	}

	// Union: everything stored + everything detected.
	names := map[string]bool{}
	for n := range stored {
		names[n] = true
	}
	for n := range detected {
		names[n] = true
	}

	out := make([]scriptSecretView, 0, len(names))
	for name := range names {
		v := scriptSecretView{Name: name}
		if entry, ok := stored[name]; ok {
			if val := entry["value"]; val != "" {
				v.Set = true
				v.Masked = maskTail(val)
			}
		}
		out = append(out, v)
	}
	// Stable order: set-first isn't important; sort by name for determinism.
	sortScriptSecretViews(out)
	return out
}

func sortScriptSecretViews(v []scriptSecretView) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j-1].Name > v[j].Name; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}
