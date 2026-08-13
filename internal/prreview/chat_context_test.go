package prreview

import (
	"context"
	"strings"
	"testing"
)

func TestChatContextBlockScoped(t *testing.T) {
	svc, _ := newService(t)
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score)
	             VALUES ('r1','src/charge.ts',50,30,5.0)`)
	prID := seedPR(t, svc, "r1", sampleDiff)
	svc.ExtractBlocks(context.Background(), prID, fakeAI{
		blockJSON: `{"title":"Add key","explanation":"adds idempotency key","importance":1}`,
		summary:   "Prevent duplicate charges",
	})
	blocks, _ := svc.Blocks(prID)

	ctxStr, err := svc.ChatContext(prID, blocks[0].ID)
	if err != nil {
		t.Fatalf("ChatContext: %v", err)
	}
	// Must carry PR summary, the block's explanation, its diff, and hot files.
	for _, want := range []string{
		"PR #247", "Prevent duplicate charges", "Currently viewing block",
		"adds idempotency key", "Diff:", "Hottest files in repo", "src/charge.ts",
	} {
		if !strings.Contains(ctxStr, want) {
			t.Errorf("context missing %q\n---\n%s", want, ctxStr)
		}
	}
}

func TestChatContextPRLevel(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r2", sampleDiff)
	ctxStr, err := svc.ChatContext(prID, 0) // no block
	if err != nil {
		t.Fatalf("ChatContext: %v", err)
	}
	if strings.Contains(ctxStr, "Currently viewing block") {
		t.Errorf("PR-level context should not reference a block:\n%s", ctxStr)
	}
	if !strings.Contains(ctxStr, "PR #247") {
		t.Errorf("expected PR header, got:\n%s", ctxStr)
	}
}
