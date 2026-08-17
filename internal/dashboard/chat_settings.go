package dashboard

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"

	"github.com/scoutapp/corral/internal/config"
)

// globalChatDir is the neutral, contained working directory the project-less
// global chat runs in — ~/.corral (corral-owned, stable, no user secrets). Falls
// back to the OS temp dir if CorralHome can't be created, so the chat still runs.
func globalChatDir() string {
	dir := config.CorralHome()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	return dir
}

// Global chat capability — how much the app-wide "Claude everywhere" dock can do.
// Stored in app_settings (DB) under chatCapabilityKey, and NULL (no row) until the
// user chooses on first run. The absence of a row is the signal the dashboard uses
// to show the first-run setup prompt; once chosen (even "readonly"), a row exists
// and we never prompt again.
//
// This governs the GLOBAL dock only. Per-project chats (/p/<id>/chat/ws) are
// unchanged — they keep their read-only default with per-session ?tools= grants.
//
// The capability decides which tools the global chat gets; the API-writes gate
// (#3) is still the real backstop for whether a mutating call actually succeeds.
const chatCapabilityKey = "global_chat_capability"

const (
	CapabilityReadOnly = "readonly" // Read/Grep/Glob — can look, can't act
	CapabilityAct      = "act"      // + Bash, so it can run `corral api` to do things
)

// ChatCapability returns the configured global-chat capability and whether it has
// been set. ok=false means "not configured yet" → the UI should prompt on first
// run. A DB/store error is treated as unset (safe: prompt rather than assume).
func (d *dashboardServer) ChatCapability() (capability string, ok bool) {
	s, err := d.getStore()
	if err != nil {
		return "", false
	}
	var v string
	err = s.DB().QueryRow(`SELECT value FROM app_settings WHERE key = ?`, chatCapabilityKey).Scan(&v)
	if err == sql.ErrNoRows || err != nil {
		return "", false
	}
	if v != CapabilityAct { // normalize anything unexpected to the safe value
		v = CapabilityReadOnly
	}
	return v, true
}

// SetChatCapability records the global-chat capability, upserting the row so a
// later change (readonly ↔ act, from Global settings) just updates it.
func (d *dashboardServer) SetChatCapability(capability string) error {
	if capability != CapabilityAct {
		capability = CapabilityReadOnly
	}
	s, err := d.getStore()
	if err != nil {
		return err
	}
	_, err = s.DB().Exec(`
		INSERT INTO app_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value,
		  updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
	`, chatCapabilityKey, capability)
	return err
}

// globalChatTools maps the capability to the tool set the global dock's Claude
// gets. Read-only can look (answer questions, read logs) but not act; act adds
// Bash so it can run `corral api` — still fenced by the API-writes gate. Unset
// defaults to read-only (the safe posture before the user has chosen).
func globalChatTools(capability string, ok bool) []string {
	if ok && capability == CapabilityAct {
		return []string{"Read", "Grep", "Glob", "Bash"}
	}
	return []string{"Read", "Grep", "Glob"}
}

// handleChatCapability reads/sets the global-chat capability.
//
//	GET /api/chat/capability → { capability: "readonly"|"act"|null, configured: bool }
//	PUT /api/chat/capability   { capability: "readonly"|"act" }
//
// GET's configured=false is what the UI keys on to show the first-run prompt.
// Setting it is a browser (session-token) action from the first-run modal or
// Global settings — it configures the assistant, so it's not itself gated (the
// API-writes gate governs what the assistant then DOES, not this preference).
func (d *dashboardServer) handleChatCapability(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cap, ok := d.ChatCapability()
		var capVal any
		if ok {
			capVal = cap
		}
		writeJSON(w, map[string]any{"capability": capVal, "configured": ok})
	case http.MethodPut:
		var body struct {
			Capability string `json:"capability"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := d.SetChatCapability(body.Capability); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cap, _ := d.ChatCapability()
		writeJSON(w, map[string]any{"capability": cap, "configured": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
