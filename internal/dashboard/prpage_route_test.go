package dashboard

import "testing"

func TestIsPRPagePath(t *testing.T) {
	page := []string{"abc123/prs/247", "repoid/prs/1"}
	notPage := []string{
		"abc123",            // bare repo (handled separately)
		"abc123/prs",        // API: list
		"abc123/prs/open",   // API: open list
		"abc123/prs/fetch",  // API: fetch
		"abc123/forensics",  // API
		"abc123/prs/12/x",   // too deep
		"abc123/prs/",       // empty number
		"abc123/prs/12a",    // non-numeric
	}
	for _, p := range page {
		if !isPRPagePath(p) {
			t.Errorf("isPRPagePath(%q) = false, want true", p)
		}
	}
	for _, p := range notPage {
		if isPRPagePath(p) {
			t.Errorf("isPRPagePath(%q) = true, want false", p)
		}
	}
}
