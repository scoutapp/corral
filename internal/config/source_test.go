package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectSourceRoundTrips(t *testing.T) {
	dir := t.TempDir()
	pd := filepath.Join(dir, ".corral", "project")
	cfg := &ProjectConfig{
		Workspace: dir,
		Source:    &ProjectSource{Kind: "pr", RepoID: "r1", Repo: "acme/x", Number: 247, URL: "https://x/pull/247", Title: "T"},
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(pd, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	got, err := ReadConfig(pd)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if got.Source == nil {
		t.Fatal("source lost on round-trip")
	}
	if got.Source.Kind != "pr" || got.Source.Number != 247 || got.Source.RepoID != "r1" || got.Source.Repo != "acme/x" {
		t.Errorf("source mismatch: %+v", got.Source)
	}

	// A project with no source stays nil (omitempty).
	plain := &ProjectConfig{Workspace: dir, CreatedAt: "x"}
	pd2 := filepath.Join(dir, "b", ".corral", "project")
	os.MkdirAll(pd2, 0o755)
	WriteConfig(pd2, plain)
	g2, _ := ReadConfig(pd2)
	if g2.Source != nil {
		t.Errorf("plain project should have nil source, got %+v", g2.Source)
	}
}
