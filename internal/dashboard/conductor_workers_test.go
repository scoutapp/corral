package dashboard

import (
	"strings"
	"testing"
)

// NOTE: the POST /api/conductor/workers endpoint was removed (the conductor does
// the work itself / delegates to a sandbox), so its validation test went with it.
// startWorkerJob + newWorkerJobID remain for internal background jobs.

// TestNewWorkerJobID sanity-checks the id shape.
func TestNewWorkerJobID(t *testing.T) {
	id := newWorkerJobID()
	if !strings.HasPrefix(id, "worker-") {
		t.Fatalf("worker id should be worker-prefixed, got %q", id)
	}
}
