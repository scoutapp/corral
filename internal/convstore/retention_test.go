//go:build sqlite_fts5

package convstore

import "testing"

// TestPruneByCount keeps only the newest N conversations and cascades their
// messages + FTS rows away.
func TestPruneByCount(t *testing.T) {
	s := newTestStore(t)

	// Seed 5 conversations, each with one searchable message.
	for i := 0; i < 5; i++ {
		id, err := s.StartConversation(ConvMeta{ConvKey: "worker:j" + itoa(i), OriginKind: "worker"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendMessage(id, Message{Role: "user", Type: "text", Text: "keyword" + itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.Count(); n != 5 {
		t.Fatalf("seeded count = %d, want 5", n)
	}

	// Keep the newest 2.
	deleted, err := s.Prune(Retention{MaxRows: 2})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted %d, want 3", deleted)
	}
	if n, _ := s.Count(); n != 2 {
		t.Fatalf("after prune count = %d, want 2", n)
	}

	// Messages of pruned conversations are gone (cascade), and so are their FTS
	// rows: the oldest keyword is no longer findable, the newest still is.
	var oldHits, newHits int
	s.db.QueryRow(`SELECT COUNT(*) FROM conv_messages_fts WHERE conv_messages_fts MATCH 'keyword0'`).Scan(&oldHits)
	s.db.QueryRow(`SELECT COUNT(*) FROM conv_messages_fts WHERE conv_messages_fts MATCH 'keyword4'`).Scan(&newHits)
	if oldHits != 0 {
		t.Errorf("pruned conversation's FTS rows should be gone, got %d", oldHits)
	}
	if newHits != 1 {
		t.Errorf("kept conversation's FTS row should remain, got %d", newHits)
	}
}

// itoa avoids importing strconv in this small test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
