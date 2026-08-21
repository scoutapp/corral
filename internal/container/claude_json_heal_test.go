package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHealClaudeJSON covers the validate-and-heal behavior that protects the
// sandbox from bind-mounting a truncated ~/.claude.json (the "Unterminated
// string" failure).
func TestHealClaudeJSON(t *testing.T) {
	good := []byte(`{"userID":"abc","projects":{}}`)
	corrupt := []byte(`{"userID":"abc","projects":{`) // truncated mid-object

	t.Run("valid file passes through and refreshes the backup", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".claude.json")
		if err := os.WriteFile(path, good, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := healClaudeJSON(path); got != path {
			t.Fatalf("healClaudeJSON = %q, want %q", got, path)
		}
		bak, err := os.ReadFile(path + ".bak")
		if err != nil {
			t.Fatalf("backup not written: %v", err)
		}
		if !json.Valid(bak) {
			t.Fatal("backup is not valid JSON")
		}
	})

	t.Run("corrupt file healed from a valid backup", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".claude.json")
		if err := os.WriteFile(path, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".bak", good, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := healClaudeJSON(path); got != path {
			t.Fatalf("healClaudeJSON = %q, want %q (should heal from backup)", got, path)
		}
		// The live file must now be valid (restored from the backup).
		data, _ := os.ReadFile(path)
		if !json.Valid(data) {
			t.Fatal("live file was not healed to valid JSON")
		}
	})

	t.Run("corrupt file with no usable backup skips the mount", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".claude.json")
		if err := os.WriteFile(path, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := healClaudeJSON(path); got != "" {
			t.Fatalf("healClaudeJSON = %q, want \"\" (skip mount)", got)
		}
	})

	t.Run("missing file skips the mount", func(t *testing.T) {
		dir := t.TempDir()
		if got := healClaudeJSON(filepath.Join(dir, ".claude.json")); got != "" {
			t.Fatalf("healClaudeJSON = %q, want \"\" for a missing file", got)
		}
	})
}
