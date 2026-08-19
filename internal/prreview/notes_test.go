package prreview

import "testing"

func TestPRNotesCRUD(t *testing.T) {
	svc, _ := newService(t)
	pr := seedPRWithDiff(t, svc, "r1", 1, diffA)

	// Empty to start.
	if notes, err := svc.Notes(pr); err != nil || len(notes) != 0 {
		t.Fatalf("expected no notes initially, got %v err=%v", notes, err)
	}

	// Add two; newest should come first.
	if _, err := svc.AddNote(pr, "first note", "cli"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	second, err := svc.AddNote(pr, "second note", "")
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if second.CreatedAt == "" {
		t.Error("AddNote should return a created_at from the DB default")
	}

	notes, err := svc.Notes(pr)
	if err != nil {
		t.Fatalf("Notes: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(notes))
	}
	if notes[0].Body != "second note" {
		t.Errorf("expected newest-first ordering; got %q first", notes[0].Body)
	}
	if notes[1].Author != "cli" {
		t.Errorf("author not persisted: %+v", notes[1])
	}

	// Blank body rejected.
	if _, err := svc.AddNote(pr, "   ", ""); err == nil {
		t.Error("expected error for blank note body")
	}

	// Remove one; unknown id errors.
	if err := svc.RemoveNote(second.ID); err != nil {
		t.Fatalf("RemoveNote: %v", err)
	}
	if err := svc.RemoveNote(999999); err == nil {
		t.Error("expected error removing unknown note")
	}
	if notes, _ := svc.Notes(pr); len(notes) != 1 {
		t.Fatalf("expected 1 note after remove, got %d", len(notes))
	}
}
