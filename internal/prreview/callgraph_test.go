package prreview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initCallgraphRepo builds a git repo with a known call structure and returns
// its bare-mirror-equivalent .git dir. bar() is called by 3 callers.
func initCallgraphRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q")
	// Go: three callers of bar, one of baz.
	write("main.go", `package main
func bar() {}
func baz() {}
func a() { bar() }
func b() { bar(); baz() }
func c() { bar() }
`)
	// Python: hot() called twice.
	write("util.py", `def hot():
    pass
def one():
    hot()
def two():
    hot()
`)
	run("add", "-A")
	run("commit", "-qm", "init")
	return filepath.Join(dir, ".git")
}

func TestBuildCallgraph(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gitDir := initCallgraphRepo(t)
	svc, _ := newService(t)

	nodes, edges, err := svc.BuildCallgraph(context.Background(), "r1", gitDir, "")
	if err != nil {
		t.Fatalf("BuildCallgraph: %v", err)
	}
	if nodes == 0 || edges == 0 {
		t.Fatalf("expected nodes and edges, got nodes=%d edges=%d", nodes, edges)
	}

	// bar has in-degree 3 (a, b, c); main.go's max in-degree should be 3.
	indeg, err := svc.InDegrees("r1")
	if err != nil {
		t.Fatalf("InDegrees: %v", err)
	}
	if indeg["main.go"] != 3 {
		t.Errorf("main.go max in-degree = %d, want 3 (bar called by a,b,c)", indeg["main.go"])
	}
	if indeg["util.py"] != 2 {
		t.Errorf("util.py max in-degree = %d, want 2 (hot called by one,two)", indeg["util.py"])
	}

	// Re-running replaces rather than appends.
	nodes2, _, err := svc.BuildCallgraph(context.Background(), "r1", gitDir, "")
	if err != nil {
		t.Fatalf("re-build: %v", err)
	}
	if nodes2 != nodes {
		t.Errorf("node count changed on rebuild: %d then %d", nodes, nodes2)
	}
	var total int
	svc.db.QueryRow(`SELECT COUNT(*) FROM pr_cg_nodes WHERE repo_id='r1'`).Scan(&total)
	if total != nodes {
		t.Errorf("rebuild left %d node rows, want %d (replace, not append)", total, nodes)
	}
}

func TestHotnessUsesInDegree(t *testing.T) {
	svc, _ := newService(t)
	// charge.ts churn 5, and give it callgraph in-degree via seeded nodes/edges.
	svc.db.Exec(`INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score)
	             VALUES ('r1','src/charge.ts',50,30,5.0)`)
	// Node in charge.ts called by 2 others → in-degree 2.
	svc.db.Exec(`INSERT INTO pr_cg_nodes (id, repo_id, file_path, symbol_name, kind, line_start, line_end)
	             VALUES (1,'r1','src/charge.ts','chargeCustomer','function',47,89)`)
	svc.db.Exec(`INSERT INTO pr_cg_nodes (id, repo_id, file_path, symbol_name, kind, line_start, line_end)
	             VALUES (2,'r1','src/a.ts','a','function',1,3),(3,'r1','src/b.ts','b','function',1,3)`)
	svc.db.Exec(`INSERT INTO pr_cg_edges (repo_id, caller_id, callee_id) VALUES ('r1',2,1),('r1',3,1)`)

	prID := seedPR(t, svc, "r1", sampleDiff)
	blocks, err := svc.ExtractBlocks(context.Background(), prID, fakeAI{
		blockJSON: `{"title":"t","explanation":"e","importance":3}`, summary: "s",
	})
	if err != nil {
		t.Fatalf("ExtractBlocks: %v", err)
	}
	var charge *Block
	for i := range blocks {
		if blocks[i].FilePath == "src/charge.ts" {
			charge = &blocks[i]
		}
	}
	if charge == nil || charge.HotnessScore == nil {
		t.Fatal("charge.ts block missing")
	}
	// churn 5 × (6-3) × (1+2) = 45. Without in-degree it would be 15.
	if *charge.HotnessScore < 40 {
		t.Errorf("hotness = %.1f, expected in-degree amplification (~45)", *charge.HotnessScore)
	}
}
