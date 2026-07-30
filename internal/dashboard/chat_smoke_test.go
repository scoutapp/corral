package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestChatSmoke exercises the chat panel handlers end to end against the real
// routes() mux: the page renders with the "not sandboxed" warning, and the WS
// upgrade actually spawns a host `claude` (skipped if claude isn't installed).
func TestChatSmoke(t *testing.T) {
	// Isolate the project registry in a temp SANDCLAUDE_HOME.
	home := t.TempDir()
	t.Setenv("SANDCLAUDE_HOME", home)

	ws := t.TempDir() // a throwaway workspace to spawn claude in
	if err := RegisterProject(ws); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	id := ProjectID(ws)

	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	client := &http.Client{}
	authGet := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "tok"})
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	// 1. Chat page renders and carries the "not sandboxed" warning + ws path.
	resp := authGet("/p/" + id + "/chat/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat page status = %d, want 200", resp.StatusCode)
	}
	body := make([]byte, 8192)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	page := string(body[:n])
	for _, want := range []string{"Not sandboxed", "/static/chat.js", "id=\"composer\""} {
		if !strings.Contains(page, want) {
			t.Errorf("chat page missing %q", want)
		}
	}

	// 2. Unauthenticated request is rejected (auth gate still applies).
	if r, _ := http.Get(srv.URL + "/p/" + id + "/chat/"); r != nil && r.StatusCode != http.StatusForbidden {
		t.Errorf("unauth chat page status = %d, want 403", r.StatusCode)
	}

	// 3. WS upgrade spawns claude. Skip if the host has no claude binary. (The
	// test process has the user's full PATH; the daemon-PATH problem the handler
	// guards against with SANDCLAUDE_CLAUDE_BIN is specific to the detached
	// daemon, not this in-process test.)
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed on host; skipping WS spawn check")
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/p/" + id + "/chat/ws"
	hdr := http.Header{
		"Cookie": {"sc_dash_token=tok"},
		"Origin": {srv.URL}, // same-origin check in terminalUpgrader
	}
	c, resp2, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		status := 0
		if resp2 != nil {
			status = resp2.StatusCode
		}
		t.Fatalf("chat WS dial failed (status %d): %v", status, err)
	}
	defer c.Close()

	// Send one user turn and assert we get back parsed frames: at least one
	// assistant "text" and a terminal "result", ending with "turn_end". This
	// exercises the whole pipeline — spawn `claude -p stream-json`, parse the
	// event stream, forward typed frames.
	if err := c.WriteJSON(chatClientMsg{Prompt: "Reply with exactly the word: pong"}); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(90 * time.Second))
	var sawText, sawResult, sawEnd bool
	for !sawEnd {
		var m chatServerMsg
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("read frame (text=%v result=%v): %v", sawText, sawResult, err)
		}
		switch m.Type {
		case "text":
			sawText = true
		case "result":
			sawResult = true
		case "turn_end":
			sawEnd = true
		}
	}
	if !sawText {
		t.Error("no assistant text frame received")
	}
	if !sawResult {
		t.Error("no result frame received")
	}
}
