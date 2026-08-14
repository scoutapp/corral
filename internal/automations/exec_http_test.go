package automations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookExecutor(t *testing.T) {
	var gotBody, gotCT, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		gotHeader = r.Header.Get("X-Repo")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec, _ := json.Marshal(WebhookSpec{
		URL:     srv.URL,
		Body:    `{"pr":"{{pr_number}}"}`,
		Headers: map[string]string{"X-Repo": "{{repo}}"},
	})
	res := WebhookExecutor{}.Execute(context.Background(),
		Action{Kind: KindWebhook, Spec: string(spec)},
		RunContext{Vars: map[string]string{"pr_number": "7", "repo": "acme/widget"}})

	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if gotBody != `{"pr":"7"}` {
		t.Errorf("body not substituted: %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Errorf("default content-type wrong: %q", gotCT)
	}
	if gotHeader != "acme/widget" {
		t.Errorf("header not substituted: %q", gotHeader)
	}
}

func TestSlackExecutor(t *testing.T) {
	var payload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec, _ := json.Marshal(SlackSpec{WebhookURL: srv.URL, Message: "PR {{pr_number}} approved by {{actor}}"})
	res := SlackExecutor{}.Execute(context.Background(),
		Action{Kind: KindSlack, Spec: string(spec)},
		RunContext{Vars: map[string]string{"pr_number": "42", "actor": "jack"}})

	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if payload["text"] != "PR 42 approved by jack" {
		t.Errorf("slack text not substituted: %q", payload["text"])
	}
}

func TestWebhookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("nope"))
	}))
	defer srv.Close()

	spec, _ := json.Marshal(WebhookSpec{URL: srv.URL, Body: "{}"})
	res := WebhookExecutor{}.Execute(context.Background(), Action{Kind: KindWebhook, Spec: string(spec)}, RunContext{})
	if res.Status != StatusError {
		t.Fatalf("expected error on 500, got %q", res.Status)
	}
}

func TestWebhookMissingURL(t *testing.T) {
	res := WebhookExecutor{}.Execute(context.Background(),
		Action{Kind: KindWebhook, Spec: `{"body":"{}"}`}, RunContext{})
	if res.Status != StatusError {
		t.Fatalf("expected error for missing url, got %q", res.Status)
	}
}

// fakeSecrets resolves a fixed secret set.
type fakeSecrets map[string]string

func (f fakeSecrets) Secret(name string) (string, bool) { v, ok := f[name]; return v, ok }

func TestSlackSecretURLResolution(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The webhook URL is a secret referenced by name, never in the spec.
	e := SlackExecutor{Secrets: fakeSecrets{"slack_hook": srv.URL}}
	res := e.Execute(context.Background(),
		Action{Kind: KindSlack, Spec: `{"webhookUrl":"{{secret.slack_hook}}","message":"hi {{actor}}"}`},
		RunContext{Vars: map[string]string{"actor": "jack"}})

	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if got["text"] != "hi jack" {
		t.Errorf("message not substituted: %q", got["text"])
	}
}

func TestSecretUnresolvedIsBlankNotLiteral(t *testing.T) {
	// With no resolver, {{secret.X}} must blank out (fail-closed), never leak the
	// literal placeholder into the request URL.
	out := resolveSecrets("{{secret.token}}", nil, nil)
	if out != "" {
		t.Errorf("unresolved secret should be blank, got %q", out)
	}
	// A missing name with a resolver present also blanks.
	out = resolveSecrets("x{{secret.missing}}y", nil, fakeSecrets{"other": "v"})
	if out != "xy" {
		t.Errorf("missing secret should blank, got %q", out)
	}
}

func TestSecretsThenVarsOrder(t *testing.T) {
	// A secret whose value itself looks like a {{var}} must NOT be re-expanded —
	// secrets resolve first, then vars, and the secret's content is final.
	out := resolveSecrets("{{secret.s}}", map[string]string{"inner": "LEAK"}, fakeSecrets{"s": "{{inner}}"})
	if out != "{{inner}}" {
		t.Errorf("secret content should not be re-expanded as a var, got %q", out)
	}
}
