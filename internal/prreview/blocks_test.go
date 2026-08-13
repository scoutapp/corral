package prreview

import (
	"context"
	"fmt"
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

// TestFileForensicsHottestFirst verifies the "Files changed" list is ordered by
// each file's max block hotness, so the panel leads with the highest-signal file.
func TestFileForensicsHottestFirst(t *testing.T) {
	svc, _ := newService(t)
	now := int64(1_700_000_000)
	svc.db.Exec(`INSERT INTO pr_repo_analysis (repo_id, head_sha) VALUES ('r','x')`)
	prID := seedPR(t, svc, "r", sampleDiff)

	// Two blocks on two files with different hotness (cold=1, hot=50).
	svc.db.Exec(`INSERT INTO pr_blocks (pr_id, order_index, priority, file_path, line_start, line_end, hotness_score, is_test)
	             VALUES (?,0,1,'src/cold.ts',1,2,1.0,0),(?,1,2,'src/hot.ts',1,2,50.0,0)`, prID, prID)

	stats, err := svc.FileForensics(prID, now)
	if err != nil {
		t.Fatalf("FileForensics: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("want 2 files, got %d", len(stats))
	}
	if stats[0].FilePath != "src/hot.ts" {
		t.Errorf("first file = %s, want src/hot.ts (hottest first)", stats[0].FilePath)
	}
}

// TestBlocksStaleness verifies block-ranking freshness detection: unranked when
// the repo isn't analyzed, current right after extraction, stale once the repo
// is (re)analyzed to a new sha, current again after re-extraction.
func TestBlocksStaleness(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r1", sampleDiff)

	// No analysis yet → extract → not analyzed, not stale.
	svc.ExtractBlocks(context.Background(), prID, nil)
	st, err := svc.BlocksStatusFor(prID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.RepoAnalyzed || st.Stale {
		t.Fatalf("pre-analysis: want repoAnalyzed=false stale=false, got %+v", st)
	}

	// Repo analyzed at sha AAA; existing blocks were ranked against "" → stale.
	svc.db.Exec(`INSERT INTO pr_repo_analysis (repo_id, head_sha) VALUES ('r1','AAA')`)
	st, _ = svc.BlocksStatusFor(prID)
	if !st.RepoAnalyzed || !st.Stale {
		t.Fatalf("after analyze, pre-rerank: want analyzed=true stale=true, got %+v", st)
	}

	// Re-extract now that the repo is analyzed → current.
	svc.ExtractBlocks(context.Background(), prID, nil)
	st, _ = svc.BlocksStatusFor(prID)
	if !st.RepoAnalyzed || st.Stale {
		t.Fatalf("after rerank: want analyzed=true stale=false, got %+v", st)
	}

	// Repo re-analyzed to a NEW sha → stale again.
	svc.db.Exec(`UPDATE pr_repo_analysis SET head_sha='BBB' WHERE repo_id='r1'`)
	st, _ = svc.BlocksStatusFor(prID)
	if !st.Stale {
		t.Fatalf("after re-analyze to new sha: want stale=true, got %+v", st)
	}
}

// TestRerankPreservesAI verifies Rerank refreshes hotness from current repo data
// while keeping the AI titles/explanations/edge-cases (no Claude calls), and
// clears the stale flag.
func TestRerankPreservesAI(t *testing.T) {
	svc, _ := newService(t)
	prID := seedPR(t, svc, "r1", sampleDiff)

	// 1) Extract WITH AI → AI text set, hotness = the AI risk_score (72).
	ai := fakeAI{
		blockJSON: `{"title":"Idempotency key","explanation":"adds a key param","codebase_context":"charge path","edge_cases":[{"description":"empty key","severity":"high"}],"importance":2,"risk_score":72}`,
		summary:   "Prevent dup charges",
	}
	blocks, err := svc.ExtractBlocks(context.Background(), prID, ai)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var chargeHot0 float64
	for _, b := range blocks {
		if b.FilePath == "src/charge.ts" && b.HotnessScore != nil {
			chargeHot0 = *b.HotnessScore
		}
	}

	// 2) Now analyze the repo: give charge.ts real churn + callgraph in-degree.
	svc.db.Exec(`INSERT INTO pr_repo_analysis (repo_id, head_sha) VALUES ('r1','SHA1')`)
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score, author_count)
	             VALUES ('r1','src/charge.ts',80,40,8.0,5)`)
	svc.db.Exec(`INSERT INTO pr_cg_nodes (id,repo_id,file_path,symbol_name,kind,line_start,line_end)
	             VALUES (1,'r1','src/charge.ts','charge','function',47,89),
	                    (2,'r1','src/x.ts','x','function',1,3),(3,'r1','src/y.ts','y','function',1,3)`)
	svc.db.Exec(`INSERT INTO pr_cg_edges (repo_id,caller_id,callee_id) VALUES ('r1',2,1),('r1',3,1)`)

	// Blocks are now stale (ranked against "" not SHA1).
	st, _ := svc.BlocksStatusFor(prID)
	if !st.Stale {
		t.Fatal("expected stale before rerank")
	}

	// 3) Rerank (no AI runner passed) → hotness refreshed, AI text preserved.
	reranked, err := svc.Rerank(context.Background(), prID)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	var charge *Block
	for i := range reranked {
		if reranked[i].FilePath == "src/charge.ts" {
			charge = &reranked[i]
		}
	}
	if charge == nil {
		t.Fatal("charge block missing after rerank")
	}
	// AI text preserved:
	if charge.Title != "Idempotency key" || charge.Explanation != "adds a key param" {
		t.Errorf("AI text NOT preserved: title=%q expl=%q", charge.Title, charge.Explanation)
	}
	if charge.CodebaseContext != "charge path" {
		t.Errorf("codebase context lost: %q", charge.CodebaseContext)
	}
	// AI risk score preserved as hotness (72), NOT recomputed mechanically —
	// rerank keeps the AI's informed ranking without re-running Claude.
	if chargeHot0 != 72 {
		t.Fatalf("initial AI hotness = %v, want 72 (risk_score)", chargeHot0)
	}
	if charge.HotnessScore == nil || *charge.HotnessScore != 72 {
		t.Errorf("AI risk-score hotness not preserved on rerank: was 72 now %v", charge.HotnessScore)
	}
	// Edge cases preserved:
	var ec int
	svc.db.QueryRow(`SELECT COUNT(*) FROM pr_block_edge_cases`).Scan(&ec)
	if ec == 0 {
		t.Error("edge cases lost on rerank")
	}
	// Summary preserved:
	var summary string
	svc.db.QueryRow(`SELECT short_summary FROM prs WHERE id=?`, prID).Scan(&summary)
	if summary != "Prevent dup charges" {
		t.Errorf("summary lost: %q", summary)
	}
	// No longer stale:
	st, _ = svc.BlocksStatusFor(prID)
	if st.Stale {
		t.Error("still stale after rerank")
	}
}

// riskByFileAI returns a per-file risk_score, so a test can model "churny file
// but well-guarded code = low risk" vs "calm file but dangerous change = high".
type riskByFileAI struct {
	riskByPath map[string]int
	summary    string
}

func (f riskByFileAI) Run(_ context.Context, prompt string) (string, error) {
	if strings.Contains(prompt, "Summarize this pull request") {
		return f.summary, nil
	}
	// The prompt embeds "File: <path>"; pick the matching risk.
	for path, risk := range f.riskByPath {
		if strings.Contains(prompt, "File: "+path) {
			return fmt.Sprintf(`{"title":"t","explanation":"e","importance":3,"risk_score":%d}`, risk), nil
		}
	}
	return `{"title":"t","explanation":"e","importance":3,"risk_score":10}`, nil
}

// TestAIRiskDrivesRanking is the #5617 scenario: charge.ts has HIGH churn/fixes
// (mechanically it'd rank hottest), but its change is well-guarded → AI gives it
// a LOW risk_score; a calm file's dangerous change gets a HIGH risk_score. With
// AI, the risk score wins, so the calm-but-dangerous block ranks first.
func TestAIRiskDrivesRanking(t *testing.T) {
	svc, _ := newService(t)
	// charge.ts: heavily churned/fixed (mechanical hotness would be huge).
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score, author_count)
	             VALUES ('r1','src/charge.ts',300,89,9.0,24)`)
	// calm.ts: barely touched.
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score, author_count)
	             VALUES ('r1','src/calm.ts',3,0,0.1,1)`)

	diff := "+++ b/src/charge.ts\n@@ -47,2 +47,3 @@\n+  const key = k // guarded, additive\n" +
		"+++ b/src/calm.ts\n@@ -1,2 +1,3 @@\n+  deleteAllUserData() // dangerous\n"
	prID := seedPRWithDiff(t, svc, "r1", 9, diff)

	ai := riskByFileAI{
		riskByPath: map[string]int{
			"src/charge.ts": 15, // churny file, but well-guarded change → low risk
			"src/calm.ts":   90, // calm file, but dangerous change → high risk
		},
		summary: "s",
	}
	blocks, err := svc.ExtractBlocks(context.Background(), prID, ai)
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	// The dangerous calm.ts block ranks FIRST despite charge.ts's churn.
	if blocks[0].FilePath != "src/calm.ts" {
		t.Errorf("hottest block = %s, want src/calm.ts (AI risk beats raw churn)", blocks[0].FilePath)
	}
	if blocks[0].HotnessScore == nil || *blocks[0].HotnessScore != 90 {
		t.Errorf("calm.ts hotness = %v, want 90 (its risk_score)", blocks[0].HotnessScore)
	}
	if blocks[1].HotnessScore == nil || *blocks[1].HotnessScore != 15 {
		t.Errorf("charge.ts hotness = %v, want 15 (its risk_score, not its churn)", blocks[1].HotnessScore)
	}
}
