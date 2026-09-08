package dashboard

import "strings"

import "testing"

func argsContain(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func TestBuildClaudeArgs(t *testing.T) {
	// No tools (PR-review chat), no system prompt.
	noTools := buildClaudeArgs("hi", "", nil, "")
	if argsContain(noTools, "--allowedTools") {
		t.Errorf("empty tools should omit --allowedTools, got: %v", noTools)
	}
	if argsContain(noTools, "--append-system-prompt") {
		t.Errorf("empty system prompt should omit --append-system-prompt, got: %v", noTools)
	}
	joined := strings.Join(noTools, " ")
	if !strings.Contains(joined, "-p hi") || !strings.Contains(joined, "stream-json") {
		t.Errorf("missing base args: %v", noTools)
	}

	// With tools (project chat): --allowedTools immediately followed by tools.
	withTools := buildClaudeArgs("hi", "", []string{"Read", "Grep"}, "")
	i := -1
	for k, a := range withTools {
		if a == "--allowedTools" {
			i = k
		}
	}
	if i < 0 {
		t.Fatalf("expected --allowedTools with tools, got: %v", withTools)
	}
	if i+1 >= len(withTools) || withTools[i+1] != "Read" {
		t.Errorf("--allowedTools must be followed by a tool value, got: %v", withTools)
	}

	// A system prompt (the conductor) appends --append-system-prompt + its value.
	withSys := buildClaudeArgs("hi", "RULES HERE", nil, "")
	j := -1
	for k, a := range withSys {
		if a == "--append-system-prompt" {
			j = k
		}
	}
	if j < 0 || j+1 >= len(withSys) || withSys[j+1] != "RULES HERE" {
		t.Errorf("expected --append-system-prompt followed by the prompt, got: %v", withSys)
	}

	// Session id appends --resume.
	resumed := buildClaudeArgs("hi", "", nil, "sess123")
	if !argsContain(resumed, "--resume") || !argsContain(resumed, "sess123") {
		t.Errorf("expected --resume sess123, got: %v", resumed)
	}
}
