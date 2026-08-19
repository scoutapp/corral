//go:build sqlite_fts5

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/scoutapp/corral/internal/convstore"
)

// TestConversationsAPI seeds a conversation via the store and exercises the read
// endpoints (list, facets, get, messages, in-conv search, deep search) end to end.
func TestConversationsAPI(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	// Seed one conversation with a couple messages via the capture tee.
	_, send, finalize := d.captureSend(context.Background(),
		convOrigin{Kind: "global-chat", ProjectID: "p1", ProjectLabel: "widget"}, func(chatServerMsg) error { return nil })
	if send == nil {
		t.Fatal("capture unavailable")
	}
	send(chatServerMsg{Type: "text", Text: "the migration needs an index"})
	send(chatServerMsg{Type: "tool_use", Tool: "Bash", Input: `{"command":"grep index"}`})
	finalize("done")

	get := func(path string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// List returns the conversation.
	code, body := get("/api/conversations")
	if code != 200 {
		t.Fatalf("list: %d %s", code, body)
	}
	var page convstore.ListPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(page.Conversations) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(page.Conversations))
	}
	convID := page.Conversations[0].ID

	// Facets include the origin + project.
	if code, body := get("/api/conversations/facets"); code != 200 || !contains(body, "global-chat") || !contains(body, "p1") {
		t.Fatalf("facets missing expected values: %d %s", code, body)
	}

	// Deep search finds it by message text; a miss returns empty.
	if _, body := get("/api/conversations/search?q=migration"); !contains(body, `"id":`+strconv.FormatInt(convID, 10)) {
		t.Fatalf("deep search did not find the conversation: %s", body)
	}
	if _, body := get("/api/conversations/search?q=zzzznotpresent"); contains(body, `"id":`+strconv.FormatInt(convID, 10)) {
		t.Fatalf("deep search should not match an absent term: %s", body)
	}

	// Get one conversation.
	if code, body := get("/api/conversations/" + strconv.FormatInt(convID, 10)); code != 200 || !contains(body, `"conversation"`) {
		t.Fatalf("get conversation: %d %s", code, body)
	}
	// Unknown id → 404.
	if code, _ := get("/api/conversations/999999"); code != 404 {
		t.Fatalf("unknown conversation: want 404, got %d", code)
	}

	// Messages: all, then in-conversation search.
	if _, body := get("/api/conversations/" + strconv.FormatInt(convID, 10) + "/messages"); !contains(body, "the migration needs an index") {
		t.Fatalf("messages missing text: %s", body)
	}
	if _, body := get("/api/conversations/" + strconv.FormatInt(convID, 10) + "/messages?q=grep"); !contains(body, "grep") {
		t.Fatalf("in-conversation search missed the tool call: %s", body)
	}
}

// TestConversationChain covers cross-origin linkage: a child conversation with
// parent_conversation_id set, and Chain()/`/chain` returning the whole forest.
func TestConversationChain(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	cs, err := d.getConvStore()
	if err != nil {
		t.Fatal(err)
	}
	// Parent (a global chat) → child (a worker it spawned).
	parent, err := cs.StartConversation(convstore.ConvMeta{ConvKey: "global-chat:root", OriginKind: "global-chat"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := cs.StartConversation(convstore.ConvMeta{
		ConvKey: "worker:w1", OriginKind: "worker", ParentConversationID: parent,
	})
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := cs.StartConversation(convstore.ConvMeta{
		ConvKey: "project-chat:p1", OriginKind: "project-chat", ParentConversationID: child,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Chain from the grandchild walks up to the root and gathers everyone.
	chain, err := cs.Chain(grandchild)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len = %d, want 3 (root + child + grandchild)", len(chain))
	}
	if chain[0].ID != parent {
		t.Errorf("chain should start at the root %d, got %d", parent, chain[0].ID)
	}

	// The endpoint returns the same forest.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/conversations/"+strconv.FormatInt(child, 10)+"/chain", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !contains(body, `"id":`+strconv.FormatInt(parent, 10)) || !contains(body, `"id":`+strconv.FormatInt(grandchild, 10)) {
		t.Fatalf("/chain missing chain members: %s", body)
	}
}

func contains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }
