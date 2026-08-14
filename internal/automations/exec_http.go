package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// Webhook and Slack catalog actions. Both are outbound HTTP POSTs with the run
// context available for {{var}} substitution in the URL/body/message. They're
// the first non-capability catalog entries — the kind of additive, best-effort
// step a user attaches to a PR event ("also ping Slack when I approve").
//
// Secrets: the URL/token belongs in the credential proxy, not stored plaintext
// in the action spec. A spec may reference an env var the proxy injects (e.g.
// {{env.SLACK_WEBHOOK}}) — resolved by the caller's environment — rather than
// embedding the secret. For now the executor treats the URL as opaque; wiring
// it through the proxy is a UI concern (branch 12) that sets the value.

// httpClient is shared; a short timeout keeps a slow endpoint from hanging a
// hook chain. It uses http.DefaultTransport, which honors HTTP(S)_PROXY — so in
// the sandbox these calls route through the allowlist/credential proxy, and a
// header/url_param credential configured for the target host is injected
// transparently. (The target host must be allowlisted.)
var httpClient = &http.Client{Timeout: 15 * time.Second}

// SecretResolver returns the value of a named secret, or ("", false) if unknown.
// It lets executors substitute {{secret.NAME}} placeholders without the secret
// ever being stored in an action's spec_json. The dashboard backs it with the
// credential store; tests pass a fake. A nil resolver leaves {{secret.*}}
// placeholders blank (fail-closed — a misconfigured secret never leaks the
// literal placeholder into an outbound request).
type SecretResolver interface {
	Secret(name string) (string, bool)
}

// secretRe matches {{secret.NAME}} placeholders (distinct from context {{var}}).
var secretRe = regexp.MustCompile(`\{\{\s*secret\.([a-zA-Z_][a-zA-Z0-9_.-]*)\s*\}\}`)

// resolveSecrets applies {{var}} context substitution FIRST, then {{secret.NAME}}
// LAST. Ordering matters for safety:
//   - vars first, so a var can never be used to smuggle a {{secret.*}} reference
//     (its value is substituted before secrets are scanned).
//   - secrets last, so a secret's value (which may itself contain {{...}}) is
//     final and never re-expanded as a var.
// Unresolved/absent secrets blank out (fail-closed) — the literal placeholder
// never leaks into an outbound request.
func resolveSecrets(s string, vars map[string]string, sr SecretResolver) string {
	s = RenderTemplate(s, vars)
	return secretRe.ReplaceAllStringFunc(s, func(m string) string {
		name := secretRe.FindStringSubmatch(m)[1]
		if sr == nil {
			return ""
		}
		if v, ok := sr.Secret(name); ok {
			return v
		}
		return ""
	})
}

// --- Webhook ---------------------------------------------------------------

// WebhookSpec configures a generic HTTP POST. Body is sent as-is (after {{var}}
// substitution); ContentType defaults to application/json.
type WebhookSpec struct {
	URL         string            `json:"url"`
	Body        string            `json:"body,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// WebhookExecutor POSTs to a configured URL with the context-substituted body.
// A SecretResolver (may be nil) resolves {{secret.NAME}} placeholders in the
// url/body/headers without the secret living in the action spec.
type WebhookExecutor struct{ Secrets SecretResolver }

func (e WebhookExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec WebhookSpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: "bad webhook spec: " + err.Error()}
	}
	url := resolveSecrets(spec.URL, rc.Vars, e.Secrets)
	if url == "" {
		return StepResult{Status: StatusError, Err: "webhook url is required"}
	}
	body := resolveSecrets(spec.Body, rc.Vars, e.Secrets)
	ct := spec.ContentType
	if ct == "" {
		ct = "application/json"
	}
	headers := map[string]string{"Content-Type": ct}
	for k, v := range spec.Headers {
		headers[k] = resolveSecrets(v, rc.Vars, e.Secrets)
	}
	return doPost(ctx, url, body, headers)
}

// --- Slack -----------------------------------------------------------------

// SlackSpec configures a Slack Incoming Webhook post. Message is the text (with
// {{var}} substitution). WebhookURL is the endpoint; since a Slack webhook URL
// IS the secret (the token is in the path), reference it as
// {{secret.NAME}} and store the value in the credential store rather than
// embedding it in the spec.
type SlackSpec struct {
	WebhookURL string `json:"webhookUrl"`
	Message    string `json:"message"`
}

// SlackExecutor posts a message to a Slack Incoming Webhook.
type SlackExecutor struct{ Secrets SecretResolver }

func (e SlackExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec SlackSpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: "bad slack spec: " + err.Error()}
	}
	url := resolveSecrets(spec.WebhookURL, rc.Vars, e.Secrets)
	if url == "" {
		return StepResult{Status: StatusError, Err: "slack webhookUrl is required (set it or reference {{secret.NAME}})"}
	}
	msg := RenderTemplate(spec.Message, rc.Vars)
	payload, _ := json.Marshal(map[string]string{"text": msg})
	return doPost(ctx, url, string(payload), map[string]string{"Content-Type": "application/json"})
}

// --- shared -----------------------------------------------------------------

// doPost performs the POST and folds the response into a StepResult. A non-2xx
// status is an error (with the response body as the reason, truncated).
func doPost(ctx context.Context, url, body string, headers map[string]string) StepResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		return StepResult{Status: StatusError, Err: err.Error()}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return StepResult{Status: StatusError, Err: err.Error()}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StepResult{
			Status: StatusError,
			Err:    fmt.Sprintf("POST %d: %s", resp.StatusCode, truncate(string(respBody), 200)),
		}
	}
	return StepResult{Status: StatusOK, Output: fmt.Sprintf("POST %d", resp.StatusCode)}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
