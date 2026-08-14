package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAPISpecValid asserts the embedded spec is well-formed JSON, is
// OpenAPI 3.x, and documents the core automations paths — so a malformed edit is
// caught at test time rather than by a downstream CLI.
func TestOpenAPISpecValid(t *testing.T) {
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openapiSpec, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if doc.OpenAPI == "" || doc.OpenAPI[0] != '3' {
		t.Errorf("expected OpenAPI 3.x, got %q", doc.OpenAPI)
	}
	for _, p := range []string{"/actions", "/actions/{id}:run", "/hooks", "/flows", "/flows/{id}:run", "/runs"} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("spec missing path %q", p)
		}
	}
}

// TestOpenAPIServed checks the endpoint returns the spec as JSON.
func TestOpenAPIServed(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/openapi.json", nil)
	req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("served spec not JSON: %v", err)
	}
	if _, ok := doc["openapi"]; !ok {
		t.Error("served spec missing openapi field")
	}
}
