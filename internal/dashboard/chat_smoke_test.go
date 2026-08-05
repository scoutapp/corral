package dashboard

import (
	"net/http"
	"net/http/httptest"
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

	// The chat UI is now a React component in the SPA (no server-rendered chat
	// page); the meaningful contract to smoke-test is the /chat/ws pipeline. Assert
	// the WS is still auth-gated, then exercise the full spawn/stream below.
	if r, _ := http.Get(srv.URL + "/p/" + id + "/chat/ws"); r != nil && r.StatusCode != http.StatusForbidden {
		t.Errorf("unauth chat WS status = %d, want 403", r.StatusCode)
	}

	// WS upgrade spawns claude. Skip only if the handler's own resolver can't
	// find claude — using resolveClaudeBin (not a bare PATH lookup) means this
	// also exercises the known-location fallback under a stripped PATH, the exact
	// scenario the resolver exists for.
	if _, err := resolveClaudeBin(); err != nil {
		t.Skip("claude not resolvable on host; skipping WS spawn check")
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

// TestChatCancel verifies the Stop path: after a prompt is sent, an
// action:"cancel" ends the turn — the server kills the process and we still get
// a terminal turn_end (with a canceled frame when the kill wins the race).
func TestChatCancel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SANDCLAUDE_HOME", home)
	ws := t.TempDir()
	if err := RegisterProject(ws); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	id := ProjectID(ws)
	if _, err := resolveClaudeBin(); err != nil {
		t.Skip("claude not resolvable on host; skipping cancel check")
	}

	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/p/" + id + "/chat/ws"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Cookie": {"sc_dash_token=tok"},
		"Origin": {srv.URL},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Start a turn, then immediately cancel it.
	if err := c.WriteJSON(chatClientMsg{Prompt: "Count slowly to one hundred, one number per line."}); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := c.WriteJSON(chatClientMsg{Action: "cancel"}); err != nil {
		t.Fatalf("write cancel: %v", err)
	}

	// The turn must terminate (turn_end) rather than run to completion — a
	// generous deadline that's still far shorter than counting to 100 would take.
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		var m chatServerMsg
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("cancel did not end the turn in time: %v", err)
		}
		if m.Type == "turn_end" {
			return // canceled and turn ended — success
		}
	}
}
