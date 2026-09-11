package prreview

import (
	"context"
	"strings"
	"testing"
)

// reviewAI returns a canned markdown review and records the prompt it saw, so we
// can assert the assembled context (risk, heuristics, findings) is fed in.
type reviewAI struct {
	md         string
	lastPrompt string
}

func (r *reviewAI) Run(_ context.Context, prompt string) (string, error) {
	r.lastPrompt = prompt
	return r.md, nil
}

func TestRunFullReviewAssemblesContextAndStores(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r1", sampleDiff)
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score)
	             VALUES ('r1','src/charge.ts',50,30,5.0)`)
	// Blocks give per-block findings + the touched-files set for heuristics.
	svc.ExtractBlocks(context.Background(), prID, fakeAI{
		blockJSON: `{"title":"charge block","explanation":"adds a key","importance":1}`,
		summary:   "s",
	})
	// A stored risk verdict to fold in.
	svc.AnalyzeRisk(context.Background(), prID, riskAI{
		json: `{"meat":"adds idempotency","bugImpact":"double charges","overallRisk":"high","riskSummary":"payments change"}`,
	})

	ai := &reviewAI{md: "## Review\n- watch idempotency on retries"}
	rev, err := svc.RunFullReview(context.Background(), prID, ai)
	if err != nil {
		t.Fatalf("RunFullReview: %v", err)
	}
	if rev == nil || !strings.Contains(rev.Markdown, "idempotency") {
		t.Fatalf("unexpected review: %+v", rev)
	}
	// The assembled prompt must carry the prior analysis as context.
	for _, want := range []string{"payments change", "charge block", "churn="} {
		if !strings.Contains(ai.lastPrompt, want) {
			t.Errorf("review prompt missing context %q", want)
		}
	}

	// Stored and round-trips, with a timestamp.
	stored, err := svc.StoredReview(prID)
	if err != nil {
		t.Fatalf("StoredReview: %v", err)
	}
	if stored == nil || stored.Markdown != rev.Markdown || stored.ReviewedAt == "" {
		t.Errorf("stored review mismatch: %+v", stored)
	}
}

// A review still runs when the prereqs were never computed — the missing pieces
// become "not available" notes rather than blocking.
func TestRunFullReviewDegradesWithoutPrereqs(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r2", sampleDiff)

	ai := &reviewAI{md: "review body"}
	rev, err := svc.RunFullReview(context.Background(), prID, ai)
	if err != nil {
		t.Fatalf("RunFullReview: %v", err)
	}
	if rev == nil || rev.Markdown != "review body" {
		t.Fatalf("expected a review, got %+v", rev)
	}
	if !strings.Contains(ai.lastPrompt, "No risk analysis available") {
		t.Errorf("expected the missing-risk note in the prompt")
	}
}

func TestRunFullReviewNoAI(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r3", sampleDiff)
	if _, err := svc.RunFullReview(context.Background(), prID, nil); err == nil {
		t.Fatal("expected error when claude runner is nil")
	}
}

func TestStoredReviewAbsent(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r4", sampleDiff)
	rev, err := svc.StoredReview(prID)
	if err != nil {
		t.Fatalf("StoredReview: %v", err)
	}
	if rev != nil {
		t.Errorf("expected nil review, got %+v", rev)
	}
}
