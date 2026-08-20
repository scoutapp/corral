//go:build sqlite_fts5

package convstore

import (
	"regexp"
	"testing"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestConversationUUID covers the stable public UUID: every new conversation
// gets a well-formed v4 UUID, distinct per conversation, retrievable via UUID(),
// and IMMUTABLE across a conv_key upsert (a later StartConversation on the same
// key must not mint a new one).
func TestConversationUUID(t *testing.T) {
	s := newTestStore(t)

	id1, err := s.StartConversation(ConvMeta{ConvKey: "worker:job-1", OriginKind: "worker"})
	if err != nil {
		t.Fatalf("StartConversation: %v", err)
	}
	u1 := s.UUID(id1)
	if !uuidV4Re.MatchString(u1) {
		t.Fatalf("new conversation UUID %q is not a v4 UUID", u1)
	}

	// A second, different conversation gets a different UUID.
	id2, err := s.StartConversation(ConvMeta{ConvKey: "worker:job-2", OriginKind: "worker"})
	if err != nil {
		t.Fatalf("StartConversation 2: %v", err)
	}
	if u2 := s.UUID(id2); u2 == u1 || !uuidV4Re.MatchString(u2) {
		t.Fatalf("second conversation UUID %q collided or malformed (first %q)", u2, u1)
	}

	// Upserting the SAME conv_key returns the same row and must NOT change the UUID.
	idAgain, err := s.StartConversation(ConvMeta{ConvKey: "worker:job-1", OriginKind: "worker", ClaudeSessionID: "sess-x"})
	if err != nil {
		t.Fatalf("StartConversation upsert: %v", err)
	}
	if idAgain != id1 {
		t.Fatalf("upsert returned a different id: %d != %d", idAgain, id1)
	}
	if got := s.UUID(id1); got != u1 {
		t.Fatalf("UUID changed on upsert: %q -> %q (must be immutable)", u1, got)
	}

	// The UUID is also exposed on the read model.
	conv, err := s.Get(id1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if conv.UUID != u1 {
		t.Fatalf("Get().UUID = %q, want %q", conv.UUID, u1)
	}
}
