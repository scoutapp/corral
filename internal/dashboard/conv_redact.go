package dashboard

import (
	"strings"
	"sync"
	"time"

	"github.com/scoutapp/corral/internal/creds"
)

// Host-claude secret redaction.
//
// Host claude runs on the operator's machine with host privileges and its
// conversations are streamed to the browser AND captured to convstore. It can
// legitimately RUN/test a script (the real secret is injected into the script's
// process env — see BashExecutor), but if it `cat`s the script, dumps its env,
// greps a creds file, or an API echoes a key, the literal secret VALUE would land
// in the transcript. This redactor replaces every known secret value with
// ‹redacted› in each streamed/recorded frame, BEFORE it reaches the browser or
// the DB — so running works, but reading/debugging never leaks the value.
//
// HONEST LIMITS (documented in docs/security.md): this is VALUE-based. A script
// that transforms the secret before printing (base64, splits it, etc.) can evade
// it. Very short values are not redacted (they'd nuke common substrings). It's a
// strong guard against the common leak paths (file read, env dump, API echo), not
// a complete information-flow control.

const redactedMarker = "‹redacted›"

// redactedSend wraps a send func so every frame's text-bearing fields (Text,
// Result, Input) have known secret values stripped before delivery. The wrap is
// cheap: no-op when there are no secrets. It never drops or fails a frame.
func redactedSend(send func(chatServerMsg) error) func(chatServerMsg) error {
	return func(m chatServerMsg) error {
		m.Text = globalRedactor.redact(m.Text)
		m.Result = globalRedactor.redact(m.Result)
		m.Input = globalRedactor.redact(m.Input)
		return send(m)
	}
}

// minRedactLen: don't redact values shorter than this — a 3-char "secret" would
// blow away innocent substrings across the whole transcript. Real API keys/tokens
// are far longer; this only skips trivially-short values.
const minRedactLen = 6

// secretRedactor holds a Replacer over the current secret value set, rebuilt on a
// short TTL so newly-added secrets take effect without a restart, cheaply.
type secretRedactor struct {
	mu       sync.Mutex
	replacer *strings.Replacer
	built    time.Time
}

var globalRedactor = &secretRedactor{}

// redact replaces every known secret value in s with the redacted marker. Cheap
// (a prebuilt Replacer) and never errors; the value set is gathered best-effort,
// but the replace always runs on whatever set we have.
func (rd *secretRedactor) redact(s string) string {
	if s == "" {
		return s
	}
	r := rd.current()
	if r == nil {
		return s
	}
	return r.Replace(s)
}

// current returns the Replacer, rebuilding it if stale. Rebuild reads all script
// secrets + proxy-credential values (the host-wide secret set). Best-effort: a
// load error just yields whatever set we could gather.
func (rd *secretRedactor) current() *strings.Replacer {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	if rd.replacer != nil && time.Since(rd.built) < 5*time.Second {
		return rd.replacer
	}
	vals := gatherSecretValues()
	if len(vals) == 0 {
		rd.replacer = strings.NewReplacer() // no-op, but non-nil so we cache the empty result
		rd.built = time.Now()
		return rd.replacer
	}
	pairs := make([]string, 0, len(vals)*2)
	for _, v := range vals {
		pairs = append(pairs, v, redactedMarker)
	}
	rd.replacer = strings.NewReplacer(pairs...)
	rd.built = time.Now()
	return rd.replacer
}

// invalidate forces the next redact to rebuild the value set (call after a secret
// is added/changed so it takes effect immediately, not after the TTL).
func (rd *secretRedactor) invalidate() {
	rd.mu.Lock()
	rd.replacer = nil
	rd.mu.Unlock()
}

// gatherSecretValues collects every secret VALUE that must be kept out of
// host-claude transcripts: all script secrets (across actions) + all
// proxy-credential values (global + project). Dedup + length filter.
func gatherSecretValues() []string {
	seen := map[string]bool{}
	add := func(v string) {
		if len(v) >= minRedactLen {
			seen[v] = true
		}
	}
	// Proxy credentials (global + project).
	for _, p := range []string{creds.GlobalCredentialsPath(), creds.ProjectCredentialsPath()} {
		if m, err := creds.LoadCredsMap(p); err == nil {
			for _, entry := range m {
				add(entry["value"])
			}
		}
	}
	// Script secrets across all actions with a secrets file.
	for _, vals := range creds.AllScriptSecretValues() {
		for _, v := range vals {
			add(v)
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	return out
}
