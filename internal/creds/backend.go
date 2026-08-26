package creds

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Credential storage backends.
//
// A credential entry is {kind:name, "value":secret}. The METADATA (host, kind,
// name) always lives in the proxy-credentials.json file. Where the secret VALUE
// lives depends on the backend:
//
//   - file backend (Linux, or macOS with the Keychain unavailable / opted out):
//     the value stays INLINE in the JSON, 0600 — exactly the original behavior.
//   - keychain backend (macOS): the value is stored in the login Keychain and the
//     JSON's "value" field is OMITTED, so no plaintext secret sits on disk.
//
// What the keychain backend deliberately does NOT do: ACL-pin the items to a
// code-signed corral identity. We prototyped that (self-signed cert + Keychain
// ACL) and dropped it, on purpose:
//   - A Keychain ACL only PROMPTS a non-authorized reader, it does not hard-deny
//     — anyone who clicks "Allow" gets the secret. It's a speed bump, not a wall.
//   - Even a clean prototype produced a storm of GUI auth prompts (grant, read,
//     re-sign, re-read ≈ 9 prompts); in production that means prompts on boot, on
//     every rebuild/update, and from the detached dashboard daemon.
//   - Signing added friction (p12 import quirks, trust settings that themselves
//     prompt) for a benefit that was only prompt-gated.
// So the macOS trust boundary here is the SAME as the 0600 file: any process
// running as the same user could read the value. The wins are encryption-at-rest
// (no plaintext on disk) and no-secret-in-argv (see set-cred). The real isolation
// — secrets never enter the sandbox; injected host-side by mitmproxy — is
// unchanged and remains the load-bearing protection. See docs/security.md.

// credBackend stores/reads/deletes the secret VALUE for a (scope, host) pair.
// scope is "global" or a stable per-project key, so two projects' same-host
// secrets don't collide.
type credBackend interface {
	// name reports the backend for logging ("file" | "keychain").
	name() string
	// getValue returns the secret and ok=false if none is stored.
	getValue(scope, host string) (value string, ok bool, err error)
	// setValue stores/replaces the secret.
	setValue(scope, host, value string) error
	// deleteValue removes the secret (no error if absent).
	deleteValue(scope, host string) error
	// storesInline reports whether the VALUE is kept in the JSON file (file
	// backend) rather than out-of-band (keychain). Load/WriteCredsMap use this to
	// decide whether to strip/inject the value field.
	storesInline() bool
}

// selectedBackend is resolved once. Override with CORRAL_CREDS_BACKEND=file|keychain
// (file forces the legacy behavior — also used to keep tests hermetic).
var selectedBackend = resolveBackend()

func resolveBackend() credBackend {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORRAL_CREDS_BACKEND"))) {
	case "file":
		return fileBackend{}
	case "keychain":
		if b := newKeychainBackend(); b != nil {
			return b
		}
		// explicitly requested but unavailable → fall back rather than break.
		return fileBackend{}
	}
	// Default: keychain on macOS when available, else file.
	if runtime.GOOS == "darwin" {
		if b := newKeychainBackend(); b != nil {
			return b
		}
	}
	return fileBackend{}
}

// scopeForPath derives a stable Keychain scope from a credentials-file path:
// "global" for the global file, "script:<id>" for a per-script secrets file, else
// a per-project key from the project dir. (The value store must not collide across
// projects/scripts that use the same key name.)
func scopeForPath(path string) string {
	if path == GlobalCredentialsPath() {
		return "global"
	}
	if id, ok := scriptIDFromPath(path); ok {
		return "script:" + id
	}
	// Per-project: key on the parent project dir so it's stable for that project.
	dir := filepath.Dir(path)
	return "project:" + dir
}
