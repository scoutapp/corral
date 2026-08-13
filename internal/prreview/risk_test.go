package prreview

import (
	"context"
	"testing"
)

// riskAI returns a canned risk JSON for the risk prompt.
type riskAI struct{ json string }

func (r riskAI) Run(_ context.Context, _ string) (string, error) { return r.json, nil }

func TestAnalyzeRisk(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r1", sampleDiff)
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score)
	             VALUES ('r1','src/charge.ts',50,30,5.0)`)
	// Give the PR a block so block context is non-empty.
	svc.ExtractBlocks(context.Background(), prID, fakeAI{
		blockJSON: `{"title":"t","explanation":"adds a key","importance":1}`,
		summary:   "s",
	})

	ai := riskAI{json: `{"meat":"adds idempotency","bugImpact":"double charges","fileHealth":[{"file":"src/charge.ts","risk":"high","insight":"payments path"}],"fixHistory":"frequent fixes","overallRisk":"high","riskSummary":"payments change, review carefully"}`}

	v, err := svc.AnalyzeRisk(context.Background(), prID, ai)
	if err != nil {
		t.Fatalf("AnalyzeRisk: %v", err)
	}
	if v.OverallRisk != "high" || v.Meat != "adds idempotency" {
		t.Fatalf("unexpected verdict: %+v", v)
	}
	if len(v.FileHealth) != 1 || v.FileHealth[0].File != "src/charge.ts" {
		t.Errorf("file health: %+v", v.FileHealth)
	}

	// Stored and round-trips.
	stored, err := svc.StoredRisk(prID)
	if err != nil {
		t.Fatalf("StoredRisk: %v", err)
	}
	if stored == nil || stored.RiskSummary != "payments change, review carefully" {
		t.Errorf("stored verdict mismatch: %+v", stored)
	}
}

func TestAnalyzeRiskNoAI(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r2", sampleDiff)
	if _, err := svc.AnalyzeRisk(context.Background(), prID, nil); err == nil {
		t.Fatal("expected error when claude runner is nil")
	}
}

func TestStoredRiskAbsent(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r3", sampleDiff)
	v, err := svc.StoredRisk(prID)
	if err != nil {
		t.Fatalf("StoredRisk: %v", err)
	}
	if v != nil {
		t.Errorf("expected nil verdict for un-analyzed PR, got %+v", v)
	}
}
