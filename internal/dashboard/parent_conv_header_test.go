package dashboard

import (
	"net/http"
	"testing"
)

// TestParentConvFromRequest covers the header parse that threads a parent
// conversation id into spawned work (workers + one-shot analyses), so those
// conversations chain back to the chat that triggered them.
func TestParentConvFromRequest(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   int64
	}{
		{"absent", "", 0},
		{"valid", "42", 42},
		{"garbage", "not-a-number", 0},
		{"negative rejected", "-5", 0},
		{"zero", "0", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "/api/prs/1/enrich", nil)
			if c.header != "" {
				r.Header.Set("X-Corral-Parent-Conversation", c.header)
			}
			if got := parentConvFromRequest(r); got != c.want {
				t.Fatalf("parentConvFromRequest(%q) = %d, want %d", c.header, got, c.want)
			}
		})
	}
	// A nil request must not panic.
	if got := parentConvFromRequest(nil); got != 0 {
		t.Fatalf("parentConvFromRequest(nil) = %d, want 0", got)
	}
}
