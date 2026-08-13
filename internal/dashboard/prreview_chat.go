package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/scoutapp/corral/internal/prreview"
)

// handleBlockChatWS runs a PR-review chat over a WebSocket, scoped to a PR and
// optionally a block. It mirrors handleChatWS's transport (host `claude` in
// stream-json mode, --resume for multi-turn) but injects PR/block context —
// summary, the current block's diff + explanation, repo hot files — into the
// FIRST turn's prompt (corral's chat passes context via the prompt, not a
// system flag).
//
//	GET /prs/<prId>/chat/ws?block=<blockId>
//
// Read-only: the assistant reasons about the injected diff/context, so it needs
// no tools. Unlike the project "Ask Claude" panel this isn't tied to a
// workspace on disk.
func (d *dashboardServer) handleBlockChatWS(w http.ResponseWriter, r *http.Request, prID int64) {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		http.Error(w, "the `claude` CLI could not be located — install Claude Code and restart the dashboard", http.StatusBadGateway)
		return
	}
	blockID := int64(0)
	if q := r.URL.Query().Get("block"); q != "" {
		blockID, _ = strconv.ParseInt(q, 10, 64)
	}

	// Build the context preamble once; prepended to the first turn only.
	var preamble string
	if s, err := d.getStore(); err == nil {
		if ctxStr, err := prreview.New(s).ChatContext(prID, blockID); err == nil {
			preamble = ctxStr
		}
	}

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(m chatServerMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(m)
	}

	var turnMu sync.Mutex
	var turnCancel context.CancelFunc
	var busy bool
	sessionID := ""
	firstTurn := true

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			turnMu.Lock()
			if turnCancel != nil {
				turnCancel()
			}
			turnMu.Unlock()
			return
		}
		var msg chatClientMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.Action == "cancel" {
			turnMu.Lock()
			if turnCancel != nil {
				turnCancel()
			}
			turnMu.Unlock()
			continue
		}
		turnMu.Lock()
		running := busy
		turnMu.Unlock()
		if running || strings.TrimSpace(msg.Prompt) == "" {
			continue
		}

		// Inject context on the first turn; later turns rely on --resume.
		prompt := msg.Prompt
		if firstTurn && preamble != "" {
			prompt = preamble + "\n\n---\n\nUser question: " + msg.Prompt
		}
		firstTurn = false

		ctx, cancel := context.WithCancel(r.Context())
		turnMu.Lock()
		turnCancel = cancel
		busy = true
		turnMu.Unlock()

		go func(p string) {
			// No workspace dir and no tools: this chat reasons about injected
			// context, not files on disk.
			newSession, canceled := d.runChatTurn(ctx, claudeBin, "", nil, p, sessionID, send)
			turnMu.Lock()
			sessionID = newSession
			busy = false
			if turnCancel != nil {
				turnCancel()
				turnCancel = nil
			}
			turnMu.Unlock()
			if canceled {
				_ = send(chatServerMsg{Type: "canceled"})
			}
			_ = send(chatServerMsg{Type: "turn_end"})
		}(prompt)
	}
}
