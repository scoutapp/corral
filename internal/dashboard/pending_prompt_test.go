package dashboard

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// writePendingConfig materializes a minimal project config carrying a pending
// prompt, at <workspace>/.corral/project/config.json.
func writePendingConfig(t *testing.T, workspace, prompt string) {
	t.Helper()
	dir := config.ProjectDirFor(workspace)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteConfig(dir, &config.ProjectConfig{Workspace: workspace, PendingPrompt: prompt}); err != nil {
		t.Fatal(err)
	}
}

// TestClaimPendingPromptFiresOnce is the correctness core of headless prompt
// delivery: claimPendingPrompt must return the prompt exactly once and clear it,
// so a project's first-turn task is typed into Claude a single time even though
// the browser fires /start and /populate-prompt near-simultaneously.
func TestClaimPendingPromptFiresOnce(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("tok")
	ws := filepath.Join(t.TempDir(), "proj")
	writePendingConfig(t, ws, "do the task")

	// First claim wins.
	if got := d.claimPendingPrompt(ws); got != "do the task" {
		t.Fatalf("first claim = %q, want the prompt", got)
	}
	// Second claim gets nothing (cleared).
	if got := d.claimPendingPrompt(ws); got != "" {
		t.Errorf("second claim = %q, want empty (fire-once)", got)
	}
	// The cleared value is persisted.
	cfg, err := config.ReadConfig(config.ProjectDirFor(ws))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PendingPrompt != "" {
		t.Errorf("PendingPrompt not cleared on disk: %q", cfg.PendingPrompt)
	}
}

// TestClaimPendingPromptConcurrent proves the lock: N racing claims yield the
// prompt to EXACTLY one caller (never twice → no double-typed task; never zero).
func TestClaimPendingPromptConcurrent(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("tok")
	ws := filepath.Join(t.TempDir(), "proj")
	writePendingConfig(t, ws, "task")

	const N = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if d.claimPendingPrompt(ws) != "" {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("concurrent claims won %d times, want exactly 1", wins)
	}
}

// TestClaimPendingPromptMissing: no config / no pending prompt → empty, no panic.
func TestClaimPendingPromptMissing(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("tok")
	// Nonexistent workspace.
	if got := d.claimPendingPrompt(filepath.Join(t.TempDir(), "nope")); got != "" {
		t.Errorf("missing config claim = %q, want empty", got)
	}
	// Existing config with no pending prompt.
	ws := filepath.Join(t.TempDir(), "empty")
	writePendingConfig(t, ws, "")
	if got := d.claimPendingPrompt(ws); got != "" {
		t.Errorf("empty pending claim = %q, want empty", got)
	}
}
