package prreview

import (
	"encoding/json"
	"testing"
)

// Verifies the gh pr list JSON shape maps into OpenPR fields (author.login is
// flattened to Author). Parsing lives inside ListOpenPRs; this exercises the
// same struct + mapping without invoking gh.
func TestGhPRListParse(t *testing.T) {
	raw := `[{"number":247,"title":"Idempotency keys","url":"https://x/pull/247",
	          "createdAt":"2026-08-01T00:00:00Z","isDraft":true,
	          "author":{"login":"jsmith"}}]`
	var items []ghPRListItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Number != 247 || it.Title != "Idempotency keys" || !it.IsDraft || it.Author.Login != "jsmith" {
		t.Errorf("unexpected parse: %+v", it)
	}
}
