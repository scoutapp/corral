package ssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAvailableKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(sshDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// A real keypair: private + .pub with type + comment.
	write("id_ed25519", "PRIVATE")
	write("id_ed25519.pub", "ssh-ed25519 AAAAC3Nz... jack@example.com")
	// A private key WITHOUT a .pub → skipped (can't describe/offer).
	write("orphan", "PRIVATE")
	// Non-key files → skipped.
	write("config", "Host *\n")
	write("known_hosts", "github.com ssh-rsa ...")

	keys := AvailableKeys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 available key, got %d: %+v", len(keys), keys)
	}
	k := keys[0]
	if k.Name != "id_ed25519" {
		t.Errorf("name = %q, want id_ed25519", k.Name)
	}
	if k.Type != "ssh-ed25519" {
		t.Errorf("type = %q, want ssh-ed25519", k.Type)
	}
	if k.Comment != "jack@example.com" {
		t.Errorf("comment = %q, want jack@example.com", k.Comment)
	}
	if k.Path != filepath.Join(sshDir, "id_ed25519") {
		t.Errorf("path = %q, want the private key path", k.Path)
	}
}

func TestAvailableKeys_NoSSHDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.ssh
	if keys := AvailableKeys(); keys != nil {
		t.Errorf("expected nil for missing ~/.ssh, got %+v", keys)
	}
}
