package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestPromptLibraryEndpoint(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("sess").routes())
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, srv.URL+path, rdr)
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Create.
	resp := do("POST", "/api/prompts/library", `{"name":"My preset","template":"do {{repo}}"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == 0 || created.Name != "My preset" {
		t.Fatalf("created wrong: %+v", created)
	}

	// List.
	resp = do("GET", "/api/prompts/library", "")
	var listed struct {
		Prompts []struct {
			Name string `json:"name"`
		} `json:"prompts"`
	}
	json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if len(listed.Prompts) != 1 || listed.Prompts[0].Name != "My preset" {
		t.Fatalf("list wrong: %+v", listed)
	}

	// Reserved name rejected.
	resp = do("POST", "/api/prompts/library", `{"name":"prompt:project.start","template":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("reserved name should 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Delete.
	resp = do("DELETE", "/api/prompts/library/"+strconv.FormatInt(created.ID, 10), "")
	if resp.StatusCode != 200 {
		t.Errorf("delete status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}
