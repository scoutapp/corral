//go:build sqlite_fts5

package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/convstore"
)

// TestBuildLogAnalysisPrompt renders a transcript into a bounded prompt that
// separates text from tool calls and ends with the question.
func TestBuildLogAnalysisPrompt(t *testing.T) {
	msgs := []convstore.MessageRow{
		{Role: "user", Type: "text", Text: "why did CI fail?"},
		{Role: "assistant", Type: "tool_use", ToolName: "Bash", ToolInput: `{"command":"go test"}`},
		{Role: "user", Type: "tool_result", ToolResult: "FAIL TestX"},
	}
	p := buildLogAnalysisPrompt("worker", "fix CI", msgs, "What went wrong?")
	for _, want := range []string{"Origin: worker", "Title: fix CI", "why did CI fail?", "tool Bash", "result: FAIL TestX", "What went wrong?"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}
}

// TestAnalyzeValidation checks the endpoint's guards (which return before any
// worker is spawned): missing id → 400, unknown id → 404, wrong method → 405.
func TestAnalyzeValidation(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	do := func(method, jsonBody string) int {
		var body *strings.Reader
		if jsonBody != "" {
			body = strings.NewReader(jsonBody)
		} else {
			body = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, srv.URL+"/api/conversations/analyze", body)
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := do(http.MethodGet, ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET analyze: want 405, got %d", code)
	}
	if code := do(http.MethodPost, `{}`); code != http.StatusBadRequest {
		t.Errorf("POST no id: want 400, got %d", code)
	}
	if code := do(http.MethodPost, `{"conversationId":999999}`); code != http.StatusNotFound {
		t.Errorf("POST unknown id: want 404, got %d", code)
	}
}
