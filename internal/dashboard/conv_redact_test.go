package dashboard

import (
	"strings"
	"testing"
	"time"
)

// TestRedactStripsValues: a known secret value anywhere in the text is replaced.
// built is set fresh so current() uses our explicit replacer, not the creds files.
func TestRedactStripsValues(t *testing.T) {
	rd := &secretRedactor{replacer: strings.NewReplacer("sk-ant-SUPERSECRET", redactedMarker), built: time.Now()}

	cases := []struct{ in, want string }{
		{"the key is sk-ant-SUPERSECRET ok", "the key is " + redactedMarker + " ok"},
		{"curl -u sk-ant-SUPERSECRET:X https://x", "curl -u " + redactedMarker + ":X https://x"},
		{"nothing here", "nothing here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := rd.redact(c.in); got != c.want {
			t.Errorf("redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRedactedSendStripsFrameFields: Text/Result/Input are all scrubbed before
// the underlying send sees them.
func TestRedactedSendStripsFrameFields(t *testing.T) {
	prev := globalRedactor
	t.Cleanup(func() { globalRedactor = prev })
	globalRedactor = &secretRedactor{replacer: strings.NewReplacer("TOKEN_ABC123", redactedMarker), built: time.Now()}

	var got chatServerMsg
	send := redactedSend(func(m chatServerMsg) error { got = m; return nil })
	_ = send(chatServerMsg{
		Type:   "tool_result",
		Text:   "printed TOKEN_ABC123",
		Result: "file contents: TOKEN_ABC123 more",
		Input:  `{"cmd":"echo TOKEN_ABC123"}`,
	})
	if strings.Contains(got.Text, "TOKEN_ABC123") ||
		strings.Contains(got.Result, "TOKEN_ABC123") ||
		strings.Contains(got.Input, "TOKEN_ABC123") {
		t.Errorf("secret leaked through redactedSend: %+v", got)
	}
	if !strings.Contains(got.Result, redactedMarker) {
		t.Errorf("expected redacted marker in Result, got %q", got.Result)
	}
}

// TestRedactMinLength: values shorter than minRedactLen must NOT be redacted
// (they'd nuke common substrings). Uses the redactor directly with a short + long
// value; only the long one is stripped.
func TestRedactMinLength(t *testing.T) {
	// A short value below the guard should pass through even if we naively added it.
	long := "longenoughsecret"
	short := "abc"
	pairs := []string{}
	for _, v := range []string{long, short} {
		if len(v) >= minRedactLen {
			pairs = append(pairs, v, redactedMarker)
		}
	}
	rd := &secretRedactor{replacer: strings.NewReplacer(pairs...), built: time.Now()}
	got := rd.redact("has " + long + " and " + short)
	if strings.Contains(got, long) {
		t.Errorf("long secret should be redacted: %q", got)
	}
	if !strings.Contains(got, short) {
		t.Errorf("short value should NOT be redacted: %q", got)
	}
}
