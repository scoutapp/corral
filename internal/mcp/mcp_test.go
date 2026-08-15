package mcp

import (
	"context"
	"strings"
	"testing"
)

// Real `claude mcp list` output (captured from the host CLI).
const sampleList = `Checking MCP server health…

claude.ai Gmail: https://gmailmcp.googleapis.com/mcp/v1 - ✔ Connected
claude.ai Xero: https://mcp.xero.com/mcp - ! Needs authentication
claude.ai Scout Remote MCP: https://scoutapm.com/mcp - ! Needs authentication
linear: https://mcp.linear.app/sse - ✔ Connected
pendingsrv: https://example.com/mcp - ⏸ Pending approval
`

func TestParseList(t *testing.T) {
	got := parseList(sampleList)
	if len(got) != 5 {
		t.Fatalf("expected 5 servers, got %d: %+v", len(got), got)
	}

	// Name with spaces + a URL containing colons (https://) parses cleanly.
	gmail := got[0]
	if gmail.Name != "claude.ai Gmail" {
		t.Errorf("name = %q, want 'claude.ai Gmail'", gmail.Name)
	}
	if gmail.URL != "https://gmailmcp.googleapis.com/mcp/v1" {
		t.Errorf("url = %q", gmail.URL)
	}
	if gmail.Status != StatusConnected {
		t.Errorf("status = %q, want connected", gmail.Status)
	}
	if gmail.Transport != TransportHTTP {
		t.Errorf("transport = %q, want http", gmail.Transport)
	}

	// A multi-word name ("claude.ai Scout Remote MCP") stays intact.
	if got[2].Name != "claude.ai Scout Remote MCP" || got[2].Status != StatusNeedsAuth {
		t.Errorf("scout row wrong: %+v", got[2])
	}
	// StatusText drops the glyph.
	if got[2].StatusText != "Needs authentication" {
		t.Errorf("statusText = %q, want 'Needs authentication'", got[2].StatusText)
	}

	// /sse URL → SSE transport.
	if got[3].Transport != TransportSSE {
		t.Errorf("linear transport = %q, want sse", got[3].Transport)
	}

	// Pending approval.
	if got[4].Status != StatusPending {
		t.Errorf("pending status = %q, want pending", got[4].Status)
	}
}

func TestParseListIgnoresNoise(t *testing.T) {
	if got := parseList("Checking MCP server health…\n\n\n"); len(got) != 0 {
		t.Errorf("expected 0 servers from noise-only output, got %d", len(got))
	}
	if got := parseList(""); len(got) != 0 {
		t.Errorf("empty output should yield 0 servers, got %d", len(got))
	}
}

func TestAddBuildsCommand(t *testing.T) {
	var gotArgs []string
	c := &Client{bin: "claude", run: func(ctx context.Context, args ...string) (string, error) {
		gotArgs = args
		return "", nil
	}}
	err := c.Add(context.Background(), AddSpec{
		Name: "sentry", Transport: TransportHTTP,
		URL: "https://mcp.sentry.dev/mcp", Header: "Authorization: Bearer xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{
		"mcp add", "--scope user", "--transport http",
		"sentry", "https://mcp.sentry.dev/mcp",
		"--header Authorization: Bearer xyz",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("add args missing %q; got: %s", want, joined)
		}
	}
}

func TestAddDefaultsTransportAndValidates(t *testing.T) {
	var gotArgs []string
	c := &Client{bin: "claude", run: func(ctx context.Context, args ...string) (string, error) {
		gotArgs = args
		return "", nil
	}}
	// No transport → defaults to http.
	c.Add(context.Background(), AddSpec{Name: "x", URL: "https://x/mcp"})
	if !strings.Contains(strings.Join(gotArgs, " "), "--transport http") {
		t.Errorf("default transport should be http; got %v", gotArgs)
	}
	// Missing url → error, no command run.
	gotArgs = nil
	if err := c.Add(context.Background(), AddSpec{Name: "x"}); err == nil {
		t.Error("expected error when url missing")
	}
	if gotArgs != nil {
		t.Error("no command should run on validation failure")
	}
}

func TestRemoveBuildsCommand(t *testing.T) {
	var gotArgs []string
	c := &Client{bin: "claude", run: func(ctx context.Context, args ...string) (string, error) {
		gotArgs = args
		return "", nil
	}}
	if err := c.Remove(context.Background(), "sentry"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotArgs, " ") != "mcp remove sentry" {
		t.Errorf("remove args = %v, want [mcp remove sentry]", gotArgs)
	}
	if err := c.Remove(context.Background(), ""); err == nil {
		t.Error("expected error on empty name")
	}
}

func TestListParsesDespiteExitError(t *testing.T) {
	// `claude mcp list` can exit non-zero when a server errors but still print
	// usable rows — we should parse them rather than fail.
	c := &Client{bin: "claude", run: func(ctx context.Context, args ...string) (string, error) {
		return sampleList, context.DeadlineExceeded // pretend a non-nil error
	}}
	got, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("should have parsed rows despite the error, got err: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("expected 5 parsed servers, got %d", len(got))
	}
}
