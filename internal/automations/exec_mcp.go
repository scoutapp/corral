package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// An mcp action is AGENTIC: there's no CLI to invoke an MCP tool directly (the
// `claude mcp` command only manages servers), so an MCP step runs headless Claude
// with the named server's tools ALLOWED, gives it the rendered prompt, and
// captures Claude's answer as the step Output — which the next step consumes as
// {{steps.<key>.output}} like any other.
//
// The server must already be connected at --scope user (via the Integrations tab
// / `claude mcp add`), so headless `claude` inherits it; we just permit its tools
// with --allowedTools mcp__<server>__*. HOST-ONLY: this runs the operator's host
// `claude`; the sandbox is never involved.

// MCPSpec is the typed config of a KindMCP action.
type MCPSpec struct {
	// Server is the connected MCP server's name (as `claude mcp list` shows it).
	Server string `json:"server"`
	// Prompt is what Claude is asked to do with that server's tools; {{var}} and
	// {{steps.<key>.output}} placeholders are rendered against the run context.
	Prompt string `json:"prompt"`
	// TimeoutSec bounds the run (default mcpDefaultTimeout). Agentic steps can take
	// a while (a real tool round-trip + Claude's reasoning).
	TimeoutSec int `json:"timeoutSec,omitempty"`
}

const mcpDefaultTimeout = 180 // seconds

// MCPExecutor runs an mcp action via headless claude. ClaudeBin is the resolved
// host binary; when empty the executor reports a clear engine error rather than
// silently doing nothing.
type MCPExecutor struct {
	ClaudeBin string
	// run is the subprocess runner; overridable in tests. nil → real exec.
	run func(ctx context.Context, bin string, args ...string) (string, error)
}

func (e MCPExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec MCPSpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: "bad mcp spec: " + err.Error()}
	}
	if strings.TrimSpace(spec.Server) == "" {
		return StepResult{Status: StatusError, Err: "mcp step: server is required"}
	}
	if strings.TrimSpace(spec.Prompt) == "" {
		return StepResult{Status: StatusError, Err: "mcp step: prompt is required"}
	}
	if e.ClaudeBin == "" {
		return StepResult{Status: StatusError, Err: "mcp step: the host `claude` CLI is unavailable"}
	}

	prompt := RenderTemplate(spec.Prompt, rc.Vars)

	timeout := spec.TimeoutSec
	if timeout <= 0 {
		timeout = mcpDefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// --allowedTools permits only this server's MCP tools (mcp__<server>__*) plus
	// nothing else, so the step can't wander into Bash/Edit/etc. The server is
	// already in the user-scope config, so headless claude connects to it.
	args := []string{
		"-p", prompt,
		"--output-format", "text",
		"--allowedTools", mcpToolGlob(spec.Server),
	}

	runner := e.run
	if runner == nil {
		runner = execClaude
	}
	out, err := runner(runCtx, e.ClaudeBin, args...)
	out = strings.TrimSpace(out)
	if err != nil {
		msg := err.Error()
		if out != "" {
			msg = out
		}
		return StepResult{Status: StatusError, Output: out, Err: msg}
	}
	return StepResult{Status: StatusOK, Output: out}
}

// mcpToolGlob is the --allowedTools pattern that permits exactly one server's MCP
// tools. Claude names MCP tools mcp__<server>__<tool>; the server name is
// sanitized to the same charset Claude uses (spaces/dots → underscores).
func mcpToolGlob(server string) string {
	return "mcp__" + sanitizeServerName(server) + "__*"
}

// sanitizeServerName maps a display server name to the token Claude uses in tool
// names (lowercase alnum + underscore).
func sanitizeServerName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// execClaude runs the host claude and returns combined output.
func execClaude(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("mcp step timed out")
	}
	return string(out), err
}
