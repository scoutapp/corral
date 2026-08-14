package prreview

import (
	"context"
	"strings"
	"testing"
)

// capturingRunner records the prompt it was asked to run and returns a canned
// reply, so we can assert what the call site actually sent to Claude.
type capturingRunner struct {
	last  string
	reply string
}

func (c *capturingRunner) Run(_ context.Context, prompt string) (string, error) {
	c.last = prompt
	return c.reply, nil
}

func TestBlockPromptUsesResolverOverride(t *testing.T) {
	svc := &Service{}
	// No resolver → built-in default (contains the baked-in instruction).
	def := svc.blockPrompt("repo-A", "diff-body", "file.go", "Title", fileHeuristic{})
	if !strings.Contains(def, "Analyze this code diff block") {
		t.Fatalf("default block prompt missing baked text: %q", def)
	}
	if !strings.Contains(def, "file.go") || !strings.Contains(def, "diff-body") {
		t.Error("default should have slots filled from args")
	}

	// With a resolver that overrides the template, the call site uses it and
	// still fills the slots.
	over := svc.WithPromptResolver(func(key, repoID string, slots map[string]string) string {
		if key != promptAnalyzeBlock {
			t.Errorf("unexpected key %q", key)
		}
		if repoID != "repo-A" {
			t.Errorf("repoID not threaded: %q", repoID)
		}
		return "CUSTOM risk review of " + slots["file"] + " :: " + slots["diff"]
	})
	got := over.blockPrompt("repo-A", "diff-body", "file.go", "Title", fileHeuristic{})
	if got != "CUSTOM risk review of file.go :: diff-body" {
		t.Errorf("override not applied: %q", got)
	}
}

func TestRiskPromptOverride(t *testing.T) {
	svc := (&Service{}).WithPromptResolver(func(key, repoID string, slots map[string]string) string {
		if key != promptRisk {
			t.Errorf("unexpected key %q", key)
		}
		return "RISK[" + slots["title"] + "]"
	})
	got := svc.riskPrompt("repo-A", "My PR", "", "", "the diff")
	if got != "RISK[My PR]" {
		t.Errorf("risk override not applied: %q", got)
	}
}

func TestSummarizePROverride(t *testing.T) {
	run := &capturingRunner{reply: "short summary"}
	svc := (&Service{}).WithPromptResolver(func(key, repoID string, slots map[string]string) string {
		return "SUMMARIZE " + slots["pr_title"]
	})
	svc.summarizePR(context.Background(), run, "repo-A", "My PR", []string{"a", "b"}, "fallback")
	if run.last != "SUMMARIZE My PR" {
		t.Errorf("summary override not sent to runner: %q", run.last)
	}
}

// A nil resolver must reproduce the exact built-in prompt (no behavior change).
func TestNilResolverIsDefault(t *testing.T) {
	a := (&Service{}).riskPrompt("r", "T", "fh", "bc", "d")
	b := (&Service{}).WithPromptResolver(nil).riskPrompt("r", "T", "fh", "bc", "d")
	if a != b {
		t.Error("nil resolver should equal no resolver")
	}
	if !strings.Contains(a, "senior engineer reviewing") {
		t.Error("default risk prompt text missing")
	}
}
