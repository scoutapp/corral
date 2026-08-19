package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/scoutapp/corral/internal/prreview"
)

// PR notes are PRIVATE, local annotations stored only in Corral's DB — never
// sent to GitHub (that's what Comment does). These handlers back both the
// browser PR page and the /api/prs/{id}/notes surface.

// handlePRNotesGet: GET /prs/<id>/notes — a PR's local notes, newest first.
func (d *dashboardServer) handlePRNotesGet(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	notes, err := prreview.New(s).Notes(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"notes": notes})
}

// handlePRNoteAdd: POST /prs/<id>/notes  { "body": "...", "author?": "..." }
func (d *dashboardServer) handlePRNoteAdd(w http.ResponseWriter, r *http.Request, prID int64) {
	var body struct {
		Body   string `json:"body"`
		Author string `json:"author"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	note, err := prreview.New(s).AddNote(prID, body.Body, body.Author)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeFilesJSON(w, map[string]any{"note": note})
}

// handlePRNoteRemove: DELETE /prs/<id>/notes/<noteId>.
func (d *dashboardServer) handlePRNoteRemove(w http.ResponseWriter, r *http.Request, noteIDStr string) {
	noteID, err := strconv.ParseInt(noteIDStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := prreview.New(s).RemoveNote(noteID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}
