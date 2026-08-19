package dashboard

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/convstore"
)

// Conversation capture: a thin decorator over the chat stream's `send` callback
// that ALSO records every frame into the conversations DB, without ever delaying
// or breaking the live browser stream. It's the single seam through which all
// host-Claude conversations are captured (chat, workers, merge, PR-review,
// drafts) — the call site supplies its origin and wraps its `send`.

// convOrigin describes where a conversation came from, for linkage. Only the
// fields relevant to a given origin are set.
type convOrigin struct {
	Kind                 string // global-chat | project-chat | pr-review-chat | merge | worker | issue-draft | prompt-draft | script-draft | analysis | log-analysis | sandbox
	OriginID             string // job id / ws token / etc. (stable per logical conversation)
	ProjectID            string
	ProjectLabel         string
	RepoID               string
	PRNumber             int
	ParentConversationID int64 // populated by cross-origin linkage (later PR)
}

// convCapturer tees chat frames into the conversations DB. It lazily creates the
// conversation row on the first frame, tracks the Claude session id, and rolls
// each frame into conv_messages. All DB work is best-effort — errors are swallowed
// like applog.Log so capture can never affect the live conversation.
type convCapturer struct {
	d      *dashboardServer
	origin convOrigin
	trace  string // trace_id from ctx (for linkage)
	span   string // root span_id from ctx

	mu        sync.Mutex
	convID    int64
	started   bool
	sessionID string
}

// captureSend wraps a chat `send` so every frame is also recorded. It returns a
// convCapturer (nil when capture is unavailable), the wrapped send (a drop-in for
// the original — unchanged when capture is unavailable), and a finalize(status)
// to stamp the terminal status when the conversation ends. Capture silently
// disables rather than breaking chat if the conversations DB can't be opened.
//
// The returned capturer's recordPrompt should be called with the user's prompt
// right before each turn (the stream doesn't echo prompts back).
func (d *dashboardServer) captureSend(ctx context.Context, origin convOrigin, send func(chatServerMsg) error) (*convCapturer, func(chatServerMsg) error, func(status string)) {
	if _, err := d.getConvStore(); err != nil {
		return nil, send, func(string) {} // capture unavailable → pass through
	}
	c := &convCapturer{
		d:      d,
		origin: origin,
		trace:  applog.TraceID(ctx),
		span:   applog.SpanID(ctx),
	}
	wrapped := func(m chatServerMsg) error {
		// Deliver to the browser FIRST and return its error — a DB hiccup must
		// never delay or fail the live stream. Capture is strictly a side effect.
		err := send(m)
		c.record(m)
		return err
	}
	finalize := func(status string) {
		c.mu.Lock()
		id := c.convID
		c.mu.Unlock()
		if id == 0 {
			return
		}
		if cs, e := c.d.getConvStore(); e == nil {
			_ = cs.SetStatus(id, status)
		}
	}
	return c, wrapped, finalize
}

// record maps one chatServerMsg into the conversation DB (best-effort). The
// conversation row is created lazily on the first frame so an empty/failed turn
// leaves no stray row.
func (c *convCapturer) record(m chatServerMsg) {
	cs, err := c.d.getConvStore()
	if err != nil {
		return
	}
	id := c.ensureStarted(cs)
	if id == 0 {
		return
	}

	// Promote the Claude session id once the stream reveals it.
	if m.Type == "session" {
		if m.SessionID != "" {
			c.mu.Lock()
			changed := m.SessionID != c.sessionID
			c.sessionID = m.SessionID
			c.mu.Unlock()
			if changed {
				_ = cs.SetSessionID(id, m.SessionID)
			}
		}
		return // a session frame carries no message content
	}

	msg, keep := toConvMessage(m)
	if !keep {
		return
	}
	_ = cs.AppendMessage(id, msg)
}

// toConvMessage maps a chatServerMsg to a convstore.Message, returning keep=false
// for frames that aren't worth a row (turn_end, canceled, empty session). role is
// derived from the frame type (tool_result is a user-role turn in Claude's model;
// text/tool_use/result are assistant-side; a "text" frame with no session yet is
// the user's own prompt — but the prompt is captured separately via recordPrompt).
func toConvMessage(m chatServerMsg) (convstore.Message, bool) {
	switch m.Type {
	case "text":
		if strings.TrimSpace(m.Text) == "" {
			return convstore.Message{}, false
		}
		return convstore.Message{Role: "assistant", Type: "text", Text: m.Text}, true
	case "tool_use":
		return convstore.Message{Role: "assistant", Type: "tool_use", ToolName: m.Tool, ToolInput: m.Input}, true
	case "tool_result":
		return convstore.Message{Role: "user", Type: "tool_result", ToolResult: m.Result, IsError: m.IsError}, true
	case "result":
		return convstore.Message{Role: "assistant", Type: "result", Model: m.Model, CostUSD: m.CostUSD, IsError: m.IsError}, true
	case "error":
		return convstore.Message{Role: "assistant", Type: "error", Text: m.Text, IsError: true}, true
	default:
		// session / turn_end / canceled: not message content.
		return convstore.Message{}, false
	}
}

// recordPrompt captures the user's own prompt for a turn. The stream doesn't echo
// the prompt back, so the call site records it explicitly right before the turn.
// It ensures the conversation row exists, then inserts a user-role text message
// (distinct from the assistant-side "text" frames the stream emits). Best-effort.
func (c *convCapturer) recordPrompt(prompt string) {
	if c == nil || strings.TrimSpace(prompt) == "" {
		return
	}
	cs, err := c.d.getConvStore()
	if err != nil {
		return
	}
	id := c.ensureStarted(cs)
	if id == 0 {
		return
	}
	_ = cs.AppendMessage(id, convstore.Message{Role: "user", Type: "text", Text: prompt})
}

// ensureStarted lazily creates the conversation row (idempotent) and returns its
// id, or 0 if it couldn't be created. Caller must NOT hold c.mu.
func (c *convCapturer) ensureStarted(cs *convstore.ConvStore) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return c.convID
	}
	convKey := c.origin.Kind + ":" + c.origin.OriginID
	if c.origin.OriginID == "" {
		// No stable origin id (e.g. a WS chat before its session id): key on this
		// capturer's pointer identity so each connection is its own row.
		convKey = c.origin.Kind + ":" + ptrKey(c)
	}
	id, e := cs.StartConversation(convstore.ConvMeta{
		ConvKey:              convKey,
		OriginKind:           c.origin.Kind,
		OriginID:             c.origin.OriginID,
		ProjectID:            c.origin.ProjectID,
		ProjectLabel:         c.origin.ProjectLabel,
		RepoID:               c.origin.RepoID,
		PRNumber:             c.origin.PRNumber,
		TraceID:              c.trace,
		RootSpanID:           c.span,
		ParentConversationID: c.origin.ParentConversationID,
	})
	if e != nil {
		return 0
	}
	c.convID = id
	c.started = true
	return id
}

// ptrKey returns a stable per-instance key from the capturer's pointer, so a WS
// chat that has no origin id yet still gets one durable conversation row.
func ptrKey(c *convCapturer) string {
	return fmt.Sprintf("%p", c)
}
