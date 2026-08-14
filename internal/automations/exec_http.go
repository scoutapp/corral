package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
// hook chain. Outbound calls in the sandbox go through the allowlist proxy, so
// the target host must be allowlisted.
var httpClient = &http.Client{Timeout: 15 * time.Second}

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
type WebhookExecutor struct{}

func (WebhookExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec WebhookSpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: "bad webhook spec: " + err.Error()}
	}
	url := RenderTemplate(spec.URL, rc.Vars)
	if url == "" {
		return StepResult{Status: StatusError, Err: "webhook url is required"}
	}
	body := RenderTemplate(spec.Body, rc.Vars)
	ct := spec.ContentType
	if ct == "" {
		ct = "application/json"
	}
	headers := map[string]string{"Content-Type": ct}
	for k, v := range spec.Headers {
		headers[k] = RenderTemplate(v, rc.Vars)
	}
	return doPost(ctx, url, body, headers)
}

// --- Slack -----------------------------------------------------------------

// SlackSpec configures a Slack Incoming Webhook post. Message is the text (with
// {{var}} substitution); the URL is the webhook endpoint (secret — inject via
// the credential proxy rather than hardcoding).
type SlackSpec struct {
	WebhookURL string `json:"webhookUrl"`
	Message    string `json:"message"`
}

// SlackExecutor posts a message to a Slack Incoming Webhook.
type SlackExecutor struct{}

func (SlackExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec SlackSpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: "bad slack spec: " + err.Error()}
	}
	url := RenderTemplate(spec.WebhookURL, rc.Vars)
	if url == "" {
		return StepResult{Status: StatusError, Err: "slack webhookUrl is required"}
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
