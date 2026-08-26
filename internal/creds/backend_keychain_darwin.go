//go:build darwin

package creds

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// keychainBackend stores credential VALUES in the macOS login Keychain via
// /usr/bin/security, so no plaintext secret sits on disk. The JSON file keeps
// only metadata (host, kind, name). Items use service "corral-cred" and account
// "<scope>:<host>" so global and per-project secrets for the same host don't
// collide.
//
// No ACL pinning / code-signing — see backend.go for why. Reads rely on the
// login keychain being unlocked (the normal case for an interactive/desktop
// session), which is silent — no GUI prompts.
type keychainBackend struct {
	securityBin string
}

const keychainService = "corral-cred"

// newKeychainBackend returns a keychain backend, or nil if /usr/bin/security
// isn't available (then the caller falls back to the file backend).
func newKeychainBackend() credBackend {
	bin, err := exec.LookPath("security")
	if err != nil {
		return nil
	}
	return keychainBackend{securityBin: bin}
}

func (keychainBackend) name() string { return "keychain" }

func (keychainBackend) storesInline() bool { return false }

func (k keychainBackend) account(scope, host string) string { return scope + ":" + host }

func (k keychainBackend) getValue(scope, host string) (string, bool, error) {
	// -w prints only the password; missing item → non-zero exit (errSecItemNotFound).
	cmd := exec.Command(k.securityBin, "find-generic-password",
		"-s", keychainService, "-a", k.account(scope, host), "-w")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		// Not-found is the common, non-error case.
		if strings.Contains(errb.String(), "could not be found") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("keychain read %s: %v: %s", host, err, strings.TrimSpace(errb.String()))
	}
	// security appends a trailing newline to the -w output.
	return strings.TrimRight(out.String(), "\n"), true, nil
}

func (k keychainBackend) setValue(scope, host, value string) error {
	// -U updates if present. `security` has no stdin flag for the password: `-w
	// <value>` puts it in argv (insecure — visible in `ps`), and a bare trailing
	// `-w` PROMPTS interactively (twice, "retype"). We take the safe path — bare
	// `-w` and answer the prompt via stdin, sending the value TWICE — so the
	// secret never appears in argv / `ps` / shell history and no GUI dialog fires.
	cmd := exec.Command(k.securityBin, "add-generic-password",
		"-s", keychainService, "-a", k.account(scope, host), "-U", "-w")
	cmd.Stdin = strings.NewReader(value + "\n" + value + "\n")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain write %s: %v: %s", host, err, strings.TrimSpace(errb.String()))
	}
	return nil
}

func (k keychainBackend) deleteValue(scope, host string) error {
	cmd := exec.Command(k.securityBin, "delete-generic-password",
		"-s", keychainService, "-a", k.account(scope, host))
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if strings.Contains(errb.String(), "could not be found") {
			return nil // already gone
		}
		return fmt.Errorf("keychain delete %s: %v: %s", host, err, strings.TrimSpace(errb.String()))
	}
	return nil
}
