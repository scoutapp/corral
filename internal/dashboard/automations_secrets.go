package dashboard

import (
	"strings"

	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/creds"
	"github.com/scoutapp/corral/internal/prreview"
)

// credsSecretResolver resolves {{secret.NAME}} placeholders in webhook/slack
// actions from the proxy credential store, so secrets (Slack webhook URLs, API
// tokens) live in proxy-credentials.json (0600) rather than in an action's
// spec_json. NAME matches either a credential's stored `name` field or its host
// key — whichever the user configured — returning the entry's `value`.
//
// This keeps the automations package free of a direct creds dependency: the
// dashboard wires this resolver into the executor registry.
type credsSecretResolver struct{}

// Secret implements automations.SecretResolver.
func (credsSecretResolver) Secret(name string) (string, bool) {
	m, err := creds.LoadCredsMap(creds.ResolveCredentialsFile())
	if err != nil {
		return "", false
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for host, entry := range m {
		if strings.ToLower(host) == want || strings.ToLower(entry["name"]) == want {
			if v := entry["value"]; v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// automationsRegistry returns the executor registry with the credential-backed
// secret resolver wired in. Emit sites use this instead of DefaultRegistry so
// {{secret.*}} placeholders resolve.
func automationsRegistry() *automations.Registry {
	return automations.RegistryWithSecrets(credsSecretResolver{})
}

// promptResolver returns a prreview.PromptResolver backed by the automations
// prompt catalog, so the PR-Review AI call sites honor user prompt overrides
// (three-level: built-in → global → repo). Reads the shared store per call; a
// store error degrades to the built-in default (renderPrompt treats an empty
// return as "use fallback"). This keeps prreview decoupled from automations.
func (d *dashboardServer) promptResolver() prreview.PromptResolver {
	return func(key, repoID string, slots map[string]string) string {
		s, err := d.getStore()
		if err != nil {
			return ""
		}
		return automations.New(s).RenderPrompt(key, repoID, slots)
	}
}
