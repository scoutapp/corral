package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPromptsAPI drives the /api/prompts catalog + override lifecycle.
func TestPromptsAPI(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// Catalog lists every prompt, all at source=default initially.
	resp := do("GET", "/api/prompts", "")
	var cat struct {
		Prompts []struct {
			Key        string `json:"key"`
			Name       string `json:"name"`
			UsedWhen   string `json:"usedWhen"`
			Source     string `json:"source"`
			Overridden bool   `json:"overridden"`
			Effective  string `json:"effective"`
		} `json:"prompts"`
	}
	json.NewDecoder(resp.Body).Decode(&cat)
	resp.Body.Close()
	if len(cat.Prompts) < 9 {
		t.Fatalf("expected full catalog, got %d", len(cat.Prompts))
	}
	for _, p := range cat.Prompts {
		if p.UsedWhen == "" {
			t.Errorf("prompt %s missing usedWhen callout", p.Key)
		}
		if p.Source != "default" || p.Overridden {
			t.Errorf("prompt %s should start at default: %+v", p.Key, p)
		}
	}

	// Save a global override.
	resp = do("PUT", "/api/prompts/pr.verify", `{"template":"CUSTOM {{pr_number}}"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}
	var item struct {
		Source     string `json:"source"`
		Overridden bool   `json:"overridden"`
		Effective  string `json:"effective"`
	}
	json.NewDecoder(resp.Body).Decode(&item)
	resp.Body.Close()
	if item.Source != "global" || !item.Overridden || item.Effective != "CUSTOM {{pr_number}}" {
		t.Fatalf("override not applied: %+v", item)
	}

	// Reset removes it.
	resp = do("DELETE", "/api/prompts/pr.verify", "")
	json.NewDecoder(resp.Body).Decode(&item)
	resp.Body.Close()
	if item.Source != "default" || item.Overridden {
		t.Errorf("reset didn't restore default: %+v", item)
	}

	// Unknown key on PUT → 404.
	resp = do("PUT", "/api/prompts/nope.key", `{"template":"x"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown key PUT should 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Repo scope: a repo override wins for that repo only.
	do("PUT", "/api/prompts/pr.risk", `{"template":"G"}`).Body.Close() // global
	do("PUT", "/api/prompts/pr.risk?repo=repo-A", `{"template":"R"}`).Body.Close()

	resp = do("GET", "/api/prompts?repo=repo-A", "")
	json.NewDecoder(resp.Body).Decode(&cat)
	resp.Body.Close()
	for _, p := range cat.Prompts {
		if p.Key == "pr.risk" && (p.Source != "repo" || p.Effective != "R") {
			t.Errorf("repo scope should win: %+v", p)
		}
	}
}
