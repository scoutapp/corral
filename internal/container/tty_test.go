package container

import (
	"os"
	"testing"
)

// The regression this guards: /dev/null is a CHARACTER DEVICE, so the old
// os.ModeCharDevice check wrongly reported a detached child (stdin=/dev/null) as
// interactive — it then tried to ssh-add and died on the passphrase prompt, so a
// dashboard restart of a keyed project never came back. isInteractive must return
// false for /dev/null (and pipes), true only for a real tty.
func TestIsInteractive_DevNullIsNotInteractive(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	orig := os.Stdin
	os.Stdin = devnull
	defer func() { os.Stdin = orig }()

	if isInteractive() {
		t.Error("isInteractive() = true for /dev/null; must be false (this is the restart-hang bug)")
	}
}

func TestIsInteractive_PipeIsNotInteractive(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	if isInteractive() {
		t.Error("isInteractive() = true for a pipe; must be false")
	}
}
