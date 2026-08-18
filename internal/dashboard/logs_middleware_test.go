package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/applog"
)

// TestErrorReasonCapturedInLog verifies the log records WHY a request failed, not
// just the status. Regression: the middleware logged "POST /path → 400" with no
// reason; now the error body (http.Error text) is folded into the message + meta.
func TestErrorReasonCapturedInLog(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("tok")
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	// POST /api/actions with a malformed JSON body → handler responds 400 with a
	// "bad JSON" reason via http.Error. That reason should land in the log.
	req, _ := http.NewRequest("POST", srv.URL+"/api/actions", strings.NewReader("{not json"))
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "tok"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("setup: expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Read back the HTTP log entry for that request and assert the reason is there.
	page, err := d.applog().Query(applog.Query{Category: applog.CatHTTP, Limit: 20})
	if err != nil {
		t.Fatalf("query logs: %v", err)
	}
	var found *applog.Record
	for i := range page.Logs {
		if strings.Contains(page.Logs[i].Message, "POST /api/actions") && strings.Contains(page.Logs[i].Message, "400") {
			found = &page.Logs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no 400 log for POST /api/actions found in %d entries", len(page.Logs))
	}
	// The message should carry a reason after the status, and meta.error should be set.
	if !strings.Contains(strings.ToLower(found.Message), "json") {
		t.Errorf("log message missing the failure reason: %q", found.Message)
	}
	if !strings.Contains(found.Meta, `"error"`) {
		t.Errorf("log meta missing error reason: %s", found.Meta)
	}
}

// TestIsWebSocketPath covers the WS-path classifier that reframes long-lived
// connections as ws.open/ws.close instead of one stuck-looking request row.
func TestIsWebSocketPath(t *testing.T) {
	ws := []string{"/chat/ws", "/p/abc/terminal/ws", "/p/abc/container/ws", "/host/ws", "/p/abc/firewall/stream"}
	for _, p := range ws {
		if !isWebSocketPath(p) {
			t.Errorf("isWebSocketPath(%q) = false, want true", p)
		}
	}
	notWS := []string{"/api/flows", "/status", "/p/abc/config", "/repos"}
	for _, p := range notWS {
		if isWebSocketPath(p) {
			t.Errorf("isWebSocketPath(%q) = true, want false", p)
		}
	}
}

// TestErrorReasonEmptyForSuccess ensures success responses aren't buffered / don't
// get a spurious reason.
func TestErrorReasonEmptyForSuccess(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: 200}
	sw.Write([]byte(`{"ok":true}`))
	if r := errorReason(sw); r != "" {
		t.Errorf("success response should have no error reason, got %q", r)
	}
	if len(sw.body) != 0 {
		t.Errorf("success body should not be buffered, got %d bytes", len(sw.body))
	}
}
