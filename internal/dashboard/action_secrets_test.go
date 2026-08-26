//go:build sqlite_fts5

package dashboard

import (
	"testing"

	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/creds"
)

// TestScriptSecretsViewDetectsAndMasks: the view lists detected-but-unset vars
// from the script, and marks/masks stored ones (value never exposed).
func TestScriptSecretsViewDetectsAndMasks(t *testing.T) {
	t.Setenv("CORRAL_CREDS_BACKEND", "file")
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("tok")

	spec := `{"script":"API_KEY=\"${FRESHDESK_API_KEY:-}\"\n[ -z \"$FRESHDESK_DOMAIN\" ] && echo x"}`
	act := automations.Action{ID: 3, Kind: "bash", Spec: spec}

	// Nothing stored yet → both detected, neither set.
	view := d.scriptSecretsView(3, act)
	byName := map[string]scriptSecretView{}
	for _, v := range view {
		byName[v.Name] = v
	}
	if _, ok := byName["FRESHDESK_API_KEY"]; !ok {
		t.Fatalf("FRESHDESK_API_KEY not detected: %+v", view)
	}
	if byName["FRESHDESK_API_KEY"].Set {
		t.Error("should be unset before storing")
	}

	// Store a value → it shows Set + masked, never the raw value.
	_ = creds.WriteScriptSecrets(3, map[string]map[string]string{
		"FRESHDESK_API_KEY": {"kind": "env", "name": "FRESHDESK_API_KEY", "value": "sk-fd-SUPERSECRET-123456"},
	})
	view = d.scriptSecretsView(3, act)
	for _, v := range view {
		if v.Name == "FRESHDESK_API_KEY" {
			if !v.Set {
				t.Error("should be set after storing")
			}
			if v.Masked == "sk-fd-SUPERSECRET-123456" || v.Masked == "" {
				t.Errorf("value must be masked, got %q", v.Masked)
			}
		}
	}
}
