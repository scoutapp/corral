package prreview

import "testing"

func TestOwnerName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/payments-service":     "acme/payments-service",
		"https://github.com/acme/payments-service.git": "acme/payments-service",
		"git@github.com:rails/rails.git":                "rails/rails",
		"https://github.com/org/repo/":                  "org/repo",
		"https://gitlab.com/x/y":                        "",
		"":                                              "",
	}
	for url, want := range cases {
		if got := OwnerName(url); got != want {
			t.Errorf("OwnerName(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestUpsertPRReplacesOnConflict(t *testing.T) {
	svc, _ := newService(t)

	first, err := svc.upsertPR("r1", ghPRView{
		Number: 247, Title: "Idempotency keys", State: "OPEN",
		URL: "https://github.com/acme/x/pull/247", BaseRefOid: "aaa", HeadRefOid: "bbb",
	}, "diff-v1")
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	if first.Number != 247 || first.Title != "Idempotency keys" {
		t.Fatalf("unexpected PR: %+v", first)
	}

	// Re-fetch same PR number → update in place, same row id, new title/state.
	second, err := svc.upsertPR("r1", ghPRView{
		Number: 247, Title: "Idempotency keys (merged)", State: "MERGED",
		URL: "https://github.com/acme/x/pull/247", BaseRefOid: "aaa", HeadRefOid: "ccc",
	}, "diff-v2")
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("expected same row id on conflict, got %d then %d", first.ID, second.ID)
	}
	if second.State != "MERGED" || second.Title != "Idempotency keys (merged)" {
		t.Errorf("row not updated: %+v", second)
	}

	// Only one PR row for the repo.
	prs, _ := svc.PRs("r1")
	if len(prs) != 1 {
		t.Errorf("expected 1 PR after re-fetch, got %d", len(prs))
	}
}
