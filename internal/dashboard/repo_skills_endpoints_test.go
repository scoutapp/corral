package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRepoSkillsEndpoint(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("sess").routes())
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Create a skill for a repo.
	resp := do("POST", "/api/skills", `{"repo":"repo-1","name":"review-rules","content":"be strict"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// List it.
	resp = do("GET", "/api/skills?repo=repo-1", "")
	var listed struct {
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
	}
	json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if len(listed.Skills) != 1 || listed.Skills[0].Name != "review-rules" {
		t.Fatalf("list wrong: %+v", listed)
	}

	// Invalid name rejected.
	resp = do("POST", "/api/skills", `{"repo":"repo-1","name":"has spaces","content":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid skill name should 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Delete.
	resp = do("DELETE", "/api/skills/"+strconv.FormatInt(created.ID, 10), "")
	if resp.StatusCode != 200 {
		t.Errorf("delete status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRepoAgentContextEndpoint(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("sess").routes())
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Empty initially.
	resp := do("GET", "/api/repos/repo-1/agent-context", "")
	var got struct {
		Content string `json:"content"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Content != "" {
		t.Errorf("expected empty context, got %q", got.Content)
	}

	// Set + read back.
	resp = do("PUT", "/api/repos/repo-1/agent-context", `{"content":"# Rules\nUse tabs."}`)
	if resp.StatusCode != 200 {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do("GET", "/api/repos/repo-1/agent-context", "")
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if !strings.Contains(got.Content, "Use tabs") {
		t.Errorf("context not persisted: %q", got.Content)
	}
}
