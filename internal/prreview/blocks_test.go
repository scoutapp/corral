package prreview

import (
	"context"
	"strings"
	"testing"
)

// fakeAI returns a canned reply per prompt, keyed by a substring match.
type fakeAI struct {
	blockJSON string
	summary   string
}

func (f fakeAI) Run(_ context.Context, prompt string) (string, error) {
	if strings.Contains(prompt, "Summarize this pull request") {
		return f.summary, nil
	}
	return f.blockJSON, nil
}

const sampleDiff = `diff --git a/src/charge.ts b/src/charge.ts
--- a/src/charge.ts
+++ b/src/charge.ts
@@ -47,3 +47,5 @@ export function chargeCustomer(
-async function chargeCustomer(customerId, amount) {
+async function chargeCustomer(customerId, amount, idempotencyKey) {
+  const key = idempotencyKey;
   return stripe.charges.create({ customer: customerId, amount });
 }
diff --git a/src/charge.test.ts b/src/charge.test.ts
--- a/src/charge.test.ts
+++ b/src/charge.test.ts
@@ -10,2 +10,3 @@ describe('charge', () => {
+  it('passes idempotency key', () => {});
 })
`

func seedPR(t *testing.T, svc *Service, repoID, diff string) int64 {
	t.Helper()
	res, err := svc.db.Exec(`
		INSERT INTO prs (repo_id, pr_number, title, raw_diff, fetched_at)
		VALUES (?, 247, 'Add idempotency keys', ?, datetime('now'))`, repoID, diff)
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestExtractBlocks(t *testing.T) {
	svc, _ := newService(t)
	// Give charge.ts churn so its block outranks the test file's.
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score)
	             VALUES ('r1', 'src/charge.ts', 50, 30, 5.0)`)
	prID := seedPR(t, svc, "r1", sampleDiff)

	ai := fakeAI{
		blockJSON: `{"title":"Add idempotency key","explanation":"Adds a key param.","codebase_context":"charge path","edge_cases":[{"description":"empty key","severity":"high"}],"importance":1}`,
		summary:   "Prevent duplicate charges with idempotency keys",
	}

	blocks, err := svc.ExtractBlocks(context.Background(), prID, ai)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (charge.ts + charge.test.ts), got %d", len(blocks))
	}

	// Hottest first: charge.ts (churn 5 × importance-weight) beats the test file.
	if blocks[0].FilePath != "src/charge.ts" {
		t.Errorf("hottest block = %s, want src/charge.ts", blocks[0].FilePath)
	}
	if blocks[0].Title != "Add idempotency key" {
		t.Errorf("title = %q", blocks[0].Title)
	}
	if blocks[0].OrderIndex != 0 || blocks[0].Priority != 1 {
		t.Errorf("ranking = order %d prio %d, want 0/1", blocks[0].OrderIndex, blocks[0].Priority)
	}

	// The test file is flagged is_test.
	var testBlock *Block
	for i := range blocks {
		if blocks[i].FilePath == "src/charge.test.ts" {
			testBlock = &blocks[i]
		}
	}
	if testBlock == nil || !testBlock.IsTest {
		t.Errorf("expected charge.test.ts flagged is_test")
	}

	// Edge case persisted for the charge.ts block.
	var ecCount int
	svc.db.QueryRow(`SELECT COUNT(*) FROM pr_block_edge_cases`).Scan(&ecCount)
	if ecCount == 0 {
		t.Errorf("expected at least one edge case persisted")
	}

	// Short summary written to the PR.
	var summary string
	svc.db.QueryRow(`SELECT short_summary FROM prs WHERE id = ?`, prID).Scan(&summary)
	if summary != "Prevent duplicate charges with idempotency keys" {
		t.Errorf("summary = %q", summary)
	}

	// Re-extract replaces (no duplicate blocks).
	blocks2, err := svc.ExtractBlocks(context.Background(), prID, ai)
	if err != nil {
		t.Fatalf("re-extract: %v", err)
	}
	if len(blocks2) != 2 {
		t.Errorf("after re-extract got %d blocks, want 2", len(blocks2))
	}
}

func TestExtractBlocksNoAI(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r2", sampleDiff)

	// nil AI → placeholder analysis, blocks still created, summary = title.
	blocks, err := svc.ExtractBlocks(context.Background(), prID, nil)
	if err != nil {
		t.Fatalf("ExtractBlocks(nil ai): %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks with placeholder AI, got %d", len(blocks))
	}
	for _, b := range blocks {
		if b.Title == "" {
			t.Errorf("placeholder block missing title: %+v", b)
		}
	}
	var summary string
	svc.db.QueryRow(`SELECT short_summary FROM prs WHERE id = ?`, prID).Scan(&summary)
	if summary != "Add idempotency keys" {
		t.Errorf("fallback summary = %q, want PR title", summary)
	}
}

func TestIsTestFile(t *testing.T) {
	yes := []string{"src/x.test.ts", "a/b.spec.js", "foo_test.py", "test_foo.py",
		"pkg/tests/x.rb", "spec/models/x.rb", "internal/x_test.go", "app/__tests__/x.ts"}
	no := []string{"src/charge.ts", "lib/fixture.go", "test-utils.ts", "attest.py"}
	for _, p := range yes {
		if !isTestFile(p) {
			t.Errorf("isTestFile(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if isTestFile(p) {
			t.Errorf("isTestFile(%q) = true, want false", p)
		}
	}
}

func TestCommentOnlyDiff(t *testing.T) {
	if !isCommentOnlyDiff("@@ -1 +1 @@\n+// just a comment\n+#another") {
		t.Error("expected comment-only diff detected")
	}
	if isCommentOnlyDiff("@@ -1 +1 @@\n+realCode()\n+// comment") {
		t.Error("mixed diff should not be comment-only")
	}
}

// TestViewThenEnrich models the two-step flow: View extracts hotness-ranked
// blocks with no AI (placeholder titles); Enrich re-extracts with AI, upgrading
// titles/explanations while keeping the ranking driven by churn/callgraph.
func TestViewThenEnrich(t *testing.T) {
	svc, _ := newService(t)
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score)
	             VALUES ('r1','src/charge.ts',50,30,5.0)`)
	prID := seedPR(t, svc, "r1", sampleDiff)

	// VIEW: nil AI → placeholder analysis, blocks still ranked.
	viewed, err := svc.ExtractBlocks(context.Background(), prID, nil)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if len(viewed) == 0 {
		t.Fatal("view produced no blocks")
	}
	if viewed[0].FilePath != "src/charge.ts" {
		t.Errorf("hottest block = %s, want src/charge.ts (churn-ranked without AI)", viewed[0].FilePath)
	}
	if viewed[0].Explanation != "This block modifies the file. Claude analysis is unavailable." {
		t.Errorf("expected placeholder explanation before enrich, got %q", viewed[0].Explanation)
	}

	// ENRICH: real AI text replaces placeholders.
	enriched, err := svc.ExtractBlocks(context.Background(), prID, fakeAI{
		blockJSON: `{"title":"Add idempotency key","explanation":"adds a key param","importance":1}`,
		summary:   "Prevent duplicate charges",
	})
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if enriched[0].Explanation != "adds a key param" {
		t.Errorf("expected AI explanation after enrich, got %q", enriched[0].Explanation)
	}
	var summary string
	svc.db.QueryRow(`SELECT short_summary FROM prs WHERE id=?`, prID).Scan(&summary)
	if summary != "Prevent duplicate charges" {
		t.Errorf("summary not updated on enrich: %q", summary)
	}
}
