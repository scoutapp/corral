package prreview

import "testing"

// seedPRWithDiff inserts a PR with a given number and diff, returns its id.
func seedPRWithDiff(t *testing.T, svc *Service, repoID string, number int, diff string) int64 {
	t.Helper()
	res, err := svc.db.Exec(`
		INSERT INTO prs (repo_id, pr_number, title, raw_diff, fetched_at)
		VALUES (?, ?, ?, ?, datetime('now'))`, repoID, number, "PR", diff)
	if err != nil {
		t.Fatalf("seed pr %d: %v", number, err)
	}
	id, _ := res.LastInsertId()
	return id
}

const diffA = "+++ b/src/charge.ts\n@@ -1 +1 @@\n+x\n+++ b/src/util.ts\n@@ -1 +1 @@\n+y\n"
const diffB = "+++ b/src/charge.ts\n@@ -1 +1 @@\n+z\n"  // overlaps A on charge.ts
const diffC = "+++ b/docs/README.md\n@@ -1 +1 @@\n+w\n" // no overlap

func TestSuggestLinks(t *testing.T) {
	svc, _ := newService(t)
	a := seedPRWithDiff(t, svc, "r1", 1, diffA)
	b := seedPRWithDiff(t, svc, "r1", 2, diffB)
	seedPRWithDiff(t, svc, "r1", 3, diffC)    // no overlap → not suggested
	seedPRWithDiff(t, svc, "other", 4, diffB) // different repo → excluded

	sug, err := svc.SuggestLinks(a, 5)
	if err != nil {
		t.Fatalf("SuggestLinks: %v", err)
	}
	if len(sug) != 1 {
		t.Fatalf("expected 1 suggestion (PR #2 shares charge.ts), got %d: %+v", len(sug), sug)
	}
	if sug[0].PRID != b || sug[0].Number != 2 || sug[0].Overlap != 1 {
		t.Errorf("unexpected suggestion: %+v", sug[0])
	}
}

func TestLinkCRUDAndSuggestExclusion(t *testing.T) {
	svc, _ := newService(t)
	a := seedPRWithDiff(t, svc, "r1", 1, diffA)
	b := seedPRWithDiff(t, svc, "r1", 2, diffB)

	link, err := svc.AddLink(a, b, "tested_by", "adds coverage")
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	if link.Relationship != "tested_by" {
		t.Errorf("relationship = %q", link.Relationship)
	}

	// Unknown relationship falls back to "related".
	l2, _ := svc.AddLink(a, b, "bogus", "")
	if l2.Relationship != "related" {
		t.Errorf("expected fallback 'related', got %q", l2.Relationship)
	}

	links, err := svc.Links(a)
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if links[0].LinkedNumber != 2 {
		t.Errorf("denormalized number = %d, want 2", links[0].LinkedNumber)
	}

	// Already-linked PRs are excluded from suggestions.
	sug, _ := svc.SuggestLinks(a, 5)
	if len(sug) != 0 {
		t.Errorf("expected 0 suggestions after linking, got %d", len(sug))
	}

	// Remove one link.
	if err := svc.RemoveLink(link.ID); err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	links, _ = svc.Links(a)
	if len(links) != 1 {
		t.Errorf("after remove expected 1 link, got %d", len(links))
	}
}
