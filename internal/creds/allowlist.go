package creds

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/scoutapp/corral/internal/config"
)

// ----------------------------------------------------------------------------
// Encryption helpers (must match allowlist-proxy/main.go)
// ----------------------------------------------------------------------------

// AllowlistDeriveKey derives a 32-byte AES-256 key from the passphrase.
func AllowlistDeriveKey(passphrase string) ([32]byte, error) {
	if passphrase == "" {
		return [32]byte{}, fmt.Errorf("ALLOWLIST_KEY environment variable is not set")
	}
	return sha256.Sum256([]byte(passphrase + ":allowlist-proxy-v1")), nil
}

// AllowlistEncrypt encrypts plaintext with AES-256-GCM. Format: nonce || ciphertext.
func AllowlistEncrypt(key [32]byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// SyncEncryptedAllowlist encrypts <cwd>/.corral/allowed-domains.txt →
// allowed-domains.txt.enc using the key from .corral/project/.allowlist-key.
// It is the single source of truth for keeping the encrypted file in step with the
// plaintext, shared by startup (Run) and the firewall-reload command.
func SyncEncryptedAllowlist() error {
	projectDir := config.GetProjectDir()

	keyPath := filepath.Join(projectDir, ".allowlist-key")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read %s: %w\n\nRun 'corral init' first to generate the encryption key", keyPath, err)
	}

	key, err := AllowlistDeriveKey(strings.TrimSpace(string(keyData)))
	if err != nil {
		return err
	}

	plaintextPath := filepath.Join(config.CorralDir(), "allowed-domains.txt")
	encPath := filepath.Join(config.CorralDir(), "allowed-domains.txt.enc")

	plaintext, err := os.ReadFile(plaintextPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", plaintextPath, err)
	}

	ciphertext, err := AllowlistEncrypt(key, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	if err := os.WriteFile(encPath, ciphertext, 0644); err != nil {
		return fmt.Errorf("write %s: %w", encPath, err)
	}
	log.Printf("Encrypted allowlist written to %s", encPath)
	return nil
}
