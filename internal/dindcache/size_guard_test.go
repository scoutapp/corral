package dindcache

import (
	"os"
	"strings"
	"testing"
)

// TestSizeUsesDuNotSystemDf is a SPEED guard: volume sizing must use a direct
// `du` on the one volume, NEVER `docker system df`. `docker system df -v` scans
// EVERY volume and takes minutes on a big vfs data root — it timed out (20s) and
// returned 0, which silently defeated the size-based decision (a 15GB baseline
// looked like 0 bytes). If someone reintroduces `system df` for sizing, this
// fails. Grepping the source is crude but exactly right for "don't call the slow
// thing" — it can't be expressed as a value assertion.
func TestSizeUsesDuNotSystemDf(t *testing.T) {
	src, err := os.ReadFile("dindcache.go")
	if err != nil {
		t.Fatalf("read dindcache.go: %v", err)
	}
	s := string(src)
	if strings.Contains(s, "system") && strings.Contains(s, "\"df\"") {
		t.Errorf("volume sizing must not use `docker system df` (slow, scans all volumes) — use `du` on the single volume")
	}
	if !strings.Contains(s, "du -sb") {
		t.Errorf("expected volumeSize to measure with `du -sb` on the single mounted volume")
	}
}
