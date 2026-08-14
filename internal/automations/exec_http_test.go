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
