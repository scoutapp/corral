// Package mcp is a thin, tested wrapper over the host `claude mcp` CLI. Corral
// doesn't run its own MCP client or keep its own connection store — it drives
// Claude Code's native registry, so a server connected here is a server every
// host `claude` (including the dashboard chat) already sees, with no config
// injection. This package owns the CLI contract: building the commands and
// parsing their (human-oriented) text output into typed structs, in one place
// that's unit-tested against real output samples.
//
// HOST-ONLY. These commands run on the operator's host with their credentials.
// MCP connections and their auth live in the host's claude registry; the sandbox
// never receives them (no config mounted, MCP hosts not in its allowlist).
package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Status is a server's health as reported by `claude mcp list`.
type Status string

const (
	StatusConnected Status = "connected"    // ✔ Connected
	StatusNeedsAuth Status = "needs_auth"   // ! Needs authentication
	StatusPending   Status = "pending"      // ⏸ Pending approval
	StatusUnknown   Status = "unknown"      // anything we don't recognize
)

// Transport is an MCP server's connection type. Corral connects REMOTE servers
// (http, sse) first; stdio is local and handled later.
type Transport string

const (
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
	TransportStdio Transport = "stdio"
)

// Server is one MCP server as Corral surfaces it. URL is empty for stdio servers.
type Server struct {
	Name      string    `json:"name"`
	URL       string    `json:"url,omitempty"`
	Transport Transport `json:"transport,omitempty"`
	Status    Status    `json:"status"`
	// StatusText is the raw human string from the CLI ("Needs authentication"),
	// kept so the UI can show exactly what claude reported.
	StatusText string `json:"statusText,omitempty"`
}

// AddSpec describes a remote MCP server to connect. Header, when set, is the full
// "Authorization: Bearer …" value passed to `claude mcp add --header`.
type AddSpec struct {
	Name      string
	Transport Transport
	URL       string
	Header    string
}

// runner runs a claude command and returns combined output. Swappable in tests.
type runner func(ctx context.Context, args ...string) (string, error)

// Client wraps the host claude mcp CLI.
type Client struct {
	bin string
	run runner
}

// New returns a Client that shells out to the given claude binary (absolute path
// preferred; falls back to PATH lookup by the caller). A per-call timeout guards
// the health-checked `list`, which can hang on an unreachable server.
func New(claudeBin string) *Client {
	c := &Client{bin: claudeBin}
	c.run = c.exec
	return c
}

func (c *Client) exec(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// List returns the configured MCP servers with their current status. It runs
// `claude mcp list`, which health-checks reachable servers, so a short timeout is
// applied. Parsing is tolerant: an unrecognized line is skipped, not fatal.
func (c *Client) List(ctx context.Context) ([]Server, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := c.run(ctx, "mcp", "list")
	if err != nil {
		// `list` can exit non-zero if a server errors, yet still print usable
		// lines. Parse what we got; only fail if nothing parsed AND there's an err.
		servers := parseList(out)
		if len(servers) == 0 {
			return nil, fmt.Errorf("claude mcp list: %w (%s)", err, strings.TrimSpace(out))
		}
		return servers, nil
	}
	return parseList(out), nil
}

// Add connects a remote MCP server at --scope user, so every host claude — the
// Corral chat included — inherits it. Returns the CLI output on failure.
func (c *Client) Add(ctx context.Context, spec AddSpec) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if spec.Name == "" || spec.URL == "" {
		return fmt.Errorf("mcp add: name and url are required")
	}
	t := spec.Transport
	if t == "" {
		t = TransportHTTP
	}
	args := []string{"mcp", "add", "--scope", "user", "--transport", string(t), spec.Name, spec.URL}
	if spec.Header != "" {
		args = append(args, "--header", spec.Header)
	}
	if out, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("claude mcp add %q: %w (%s)", spec.Name, err, strings.TrimSpace(out))
	}
	return nil
}

// Remove disconnects a server by name.
func (c *Client) Remove(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if name == "" {
		return fmt.Errorf("mcp remove: name is required")
	}
	if out, err := c.run(ctx, "mcp", "remove", name); err != nil {
		return fmt.Errorf("claude mcp remove %q: %w (%s)", name, err, strings.TrimSpace(out))
	}
	return nil
}

// parseList turns `claude mcp list` output into servers. Lines look like:
//
//	claude.ai Gmail: https://gmailmcp.googleapis.com/mcp/v1 - ✔ Connected
//	claude.ai Xero: https://mcp.xero.com/mcp - ! Needs authentication
//
// The name may contain spaces and the URL contains colons, so we split the name
// off at the FIRST ": " and the status off at the LAST " - ". A leading
// "Checking MCP server health…" line and blanks are ignored.
func parseList(out string) []Server {
	var servers []Server
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "Checking") {
			continue
		}
		// name : rest
		ci := strings.Index(line, ": ")
		if ci < 0 {
			continue
		}
		name := strings.TrimSpace(line[:ci])
		rest := line[ci+2:]

		// rest = "<url> - <status>"  (status after the LAST " - ")
		si := strings.LastIndex(rest, " - ")
		var urlPart, statusPart string
		if si >= 0 {
			urlPart = strings.TrimSpace(rest[:si])
			statusPart = strings.TrimSpace(rest[si+3:])
		} else {
			urlPart = strings.TrimSpace(rest)
		}
		if name == "" {
			continue
		}
		st, text := classifyStatus(statusPart)
		servers = append(servers, Server{
			Name:       name,
			URL:        urlPart,
			Transport:  transportFromURL(urlPart),
			Status:     st,
			StatusText: text,
		})
	}
	return servers
}

// classifyStatus maps the CLI's status phrase (with its leading glyph) to a
// Status and the human text without the glyph.
func classifyStatus(s string) (Status, string) {
	// Strip a leading status glyph (✔ / ! / ⏸) and surrounding space.
	text := strings.TrimSpace(strings.TrimLeft(s, "✔!⏸ "))
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "connected"):
		return StatusConnected, text
	case strings.Contains(low, "needs authentication"), strings.Contains(low, "authenticate"):
		return StatusNeedsAuth, text
	case strings.Contains(low, "pending"):
		return StatusPending, text
	case text == "":
		return StatusUnknown, ""
	default:
		return StatusUnknown, text
	}
}

// transportFromURL infers the transport from a remote URL's path (an /sse suffix
// is SSE; otherwise HTTP). stdio servers have no URL and are inferred as stdio.
func transportFromURL(url string) Transport {
	if url == "" {
		return TransportStdio
	}
	if strings.HasSuffix(strings.ToLower(url), "/sse") || strings.Contains(strings.ToLower(url), "/sse?") {
		return TransportSSE
	}
	return TransportHTTP
}
