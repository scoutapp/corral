//go:build sqlite_fts5

package convstore

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentWritesNoLock hammers the store from many goroutines the way a
// conductor fan-out does (a burst of analyses each starting a conversation and
// appending messages at once). Before writeMu, these raced for SQLite's single
// write lock and, past _busy_timeout, returned "database is locked". With the
// in-process write serialization, every write must succeed.
func TestConcurrentWritesNoLock(t *testing.T) {
	s := newTestStore(t)

	const workers = 24
	const msgsPer = 8

	var wg sync.WaitGroup
	errs := make(chan error, workers*(msgsPer+2))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id, err := s.StartConversation(ConvMeta{
				OriginKind: "analysis",
				OriginID:   fmt.Sprintf("job-%d", w),
			})
			if err != nil {
				errs <- fmt.Errorf("worker %d start: %w", w, err)
				return
			}
			for m := 0; m < msgsPer; m++ {
				if err := s.AppendMessage(id, Message{Role: "assistant", Type: "text", Text: fmt.Sprintf("w%d m%d", w, m)}); err != nil {
					errs <- fmt.Errorf("worker %d append %d: %w", w, m, err)
					return
				}
			}
			if err := s.SetStatus(id, "done"); err != nil {
				errs <- fmt.Errorf("worker %d status: %w", w, err)
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err) // any "database is locked" (or other write error) fails the test
	}

	// Every conversation landed with all its messages.
	var convs, msgs int
	s.DB().QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&convs)
	s.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages`).Scan(&msgs)
	if convs != workers {
		t.Errorf("conversations = %d, want %d", convs, workers)
	}
	if msgs != workers*msgsPer {
		t.Errorf("messages = %d, want %d", msgs, workers*msgsPer)
	}
}
