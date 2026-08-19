package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// TestEffectiveMergeStrategy covers the repo→global→"squash" resolution and the
// clamp to GitHub-allowed methods.
func TestEffectiveMergeStrategy(t *testing.T) {
	// Isolate global settings to a temp CORRAL_HOME so the global-default cases
	// don't read the developer's real config.
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)

	writeGlobalStrategy := func(s string) {
		gs := config.ReadGlobalSettings()
		gs.MergeStrategy = s
		if err := config.WriteGlobalSettings(gs); err != nil {
			t.Fatalf("write global settings: %v", err)
		}
	}

	tests := []struct {
		name          string
		preferred     string
		globalDefault string
		allowed       []string
		want          string
	}{
		{"repo preference wins", "rebase", "squash", []string{"squash", "merge", "rebase"}, "rebase"},
		{"falls back to global", "", "merge", []string{"squash", "merge", "rebase"}, "merge"},
		{"falls back to squash when nothing set", "", "", []string{"squash", "merge", "rebase"}, "squash"},
		{"unknown allowed set: no clamp", "rebase", "", nil, "rebase"},
		{"clamp: preferred disabled on repo → first allowed", "rebase", "", []string{"squash", "merge"}, "squash"},
		{"clamp: global disabled on repo → first allowed", "", "rebase", []string{"merge"}, "merge"},
		{"allowed contains the choice → keep it", "merge", "", []string{"squash", "merge"}, "merge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writeGlobalStrategy(tc.globalDefault)
			got := effectiveMergeStrategy(tc.preferred, tc.allowed)
			if got != tc.want {
				t.Fatalf("effectiveMergeStrategy(%q, %v) with global=%q = %q, want %q",
					tc.preferred, tc.allowed, tc.globalDefault, got, tc.want)
			}
		})
	}

	// Sanity: the isolated home actually holds the settings file we wrote.
	if _, err := os.Stat(filepath.Join(home, "global-settings.json")); err != nil {
		t.Fatalf("expected global-settings.json in temp home: %v", err)
	}
}
