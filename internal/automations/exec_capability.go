package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// CapabilitySpec is the typed config of a KindCapability action. Capability
// names the provider-neutral operation; Body/Method are optional defaults that
// the run context can override via vars (so a hook can pass a per-PR body).
type CapabilitySpec struct {
	Capability string `json:"capability"` // pr-approve | pr-comment | pr-request-changes | pr-merge
	Body       string `json:"body,omitempty"`
	Method     string `json:"method,omitempty"` // merge method for pr-merge
}

// CapabilityExecutor runs KindCapability actions against a PRProvider. The
// provider is chosen once (GitHub today); swapping it in the future is the only
// change needed to target a different host.
type CapabilityExecutor struct {
	provider PRProvider
}

// NewCapabilityExecutor wires a capability executor to a provider.
func NewCapabilityExecutor(p PRProvider) CapabilityExecutor {
	return CapabilityExecutor{provider: p}
}

// Execute decodes the capability spec, resolves the PR target + body/method from
// the run context (context vars win over spec defaults), and dispatches to the
// provider. Provider failures are returned as StepResult.Status=error so the run
// is still recorded (per the runner's contract).
func (e CapabilityExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec CapabilitySpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: fmt.Sprintf("bad capability spec: %v", err)}
	}

	target, err := prTargetFromContext(rc)
	if err != nil {
		return StepResult{Status: StatusError, Err: err.Error()}
	}

	// Context vars override spec defaults (a hook passes the actual body typed
	// in the UI; the action's stored body is a fallback).
	body := spec.Body
	if v := rc.Var("body"); v != "" {
		body = v
	}
	method := spec.Method
	if v := rc.Var("method"); v != "" {
		method = v
	}

	switch spec.Capability {
	case CapApprove:
		err = e.provider.Approve(ctx, target, body)
	case CapRequestChanges:
		err = e.provider.RequestChanges(ctx, target, body)
	case CapComment:
		err = e.provider.Comment(ctx, target, body)
	case CapMerge:
		err = e.provider.Merge(ctx, target, method)
	default:
		return StepResult{Status: StatusError, Err: fmt.Sprintf("unknown capability %q", spec.Capability)}
	}
	if err != nil {
		return StepResult{Status: StatusError, Err: err.Error()}
	}
	return StepResult{Status: StatusOK, Output: fmt.Sprintf("%s via %s", spec.Capability, e.provider.Name())}
}

// prTargetFromContext builds a provider-neutral PR target from run-context vars.
// Emitters populate owner_name, pr_number, and (for line ops) head_sha.
func prTargetFromContext(rc RunContext) (PRTarget, error) {
	owner := rc.Var("owner_name")
	numStr := rc.Var("pr_number")
	if owner == "" || numStr == "" {
		return PRTarget{}, fmt.Errorf("capability needs owner_name and pr_number in context")
	}
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return PRTarget{}, fmt.Errorf("pr_number %q is not an integer", numStr)
	}
	return PRTarget{OwnerName: owner, Number: num, HeadSHA: rc.Var("head_sha")}, nil
}

// DefaultRegistry returns a Registry with the built-in executors and no secret
// resolver ({{secret.*}} placeholders resolve to empty). Use RegistryWithSecrets
// to wire the credential store.
func DefaultRegistry() *Registry {
	return RegistryWithSecrets(nil)
}

// RegistryOptions configure the built-in registry.
type RegistryOptions struct {
	Secrets    SecretResolver // {{secret.*}} resolution for webhook/slack
	LoginShell string         // run bash steps via this login shell (full PATH); "" = bash -c
}

// RegistryWithSecrets returns the built-in registry with a secret resolver wired
// into the webhook/slack executors. Kept for callers that only need secrets.
func RegistryWithSecrets(sr SecretResolver) *Registry {
	return RegistryWith(RegistryOptions{Secrets: sr})
}

// RegistryWith returns the built-in registry configured by opts. Capability
// actions use the GitHub provider by default; bash steps use opts.LoginShell.
func RegistryWith(opts RegistryOptions) *Registry {
	r := NewRegistry()
	r.Register(KindCapability, NewCapabilityExecutor(GitHubProvider{}))
	r.Register(KindClaudePrompt, PromptExecutor{})
	r.Register(KindWebhook, WebhookExecutor{Secrets: opts.Secrets})
	r.Register(KindSlack, SlackExecutor{Secrets: opts.Secrets})
	r.Register(KindBash, BashExecutor{LoginShell: opts.LoginShell})
	return r
}
