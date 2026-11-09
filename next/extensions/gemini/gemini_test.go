package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestMessagesMaxTokensLeftToModel(t *testing.T) {
	limit := 32000
	req := llm.Request{Model: "gemini-2.5-pro", Native: &llm.Native{Format: llm.FormatAnthropicMessages},
		Items:   []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Options: llm.Options{MaxOutputTokens: &limit}}
	body, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "maxOutputTokens") {
		t.Errorf("body %s carries a Messages client's max_tokens", body)
	}
	if len(tr.Losses) == 0 || tr.Losses[0].Path != "max_tokens" {
		t.Errorf("losses = %+v, want max_tokens reported", tr.Losses)
	}
	req.Native.Format = llm.FormatOpenAIChat
	body, _, _ = buildBody(req)
	if !strings.Contains(string(body), `"maxOutputTokens":32000`) {
		t.Errorf("a Chat client's max_tokens is missing from %s", body)
	}
}

func TestUpstreamErrors(t *testing.T) {
	srv := geminitest.New(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) { geminitest.RateLimited(w, 7) }
	key := srv.IssueKey()
	resp := post(t, srv.URL+"/v1beta/models/m:generateContent", key)
	if e := upstreamError(resp, "m"); e.Code != llm.CodeRateLimit || !e.Retryable || e.RetryAfterMS != 7000 || e.ProviderCode != "RESOURCE_EXHAUSTED" {
		t.Errorf("429 with RetryInfo 7s = %+v", e)
	}
	resp = post(t, srv.URL+"/v1beta/models/m:generateContent", "not-a-key")
	if e := upstreamError(resp, "m"); e.Code != llm.CodeAuth || !e.Retryable || e.Scope != llm.ScopeCredential || e.Status != http.StatusBadRequest {
		t.Errorf("400 API_KEY_INVALID = %+v, want a retryable auth error with Google's status", e)
	}
}

func post(t *testing.T, url, key string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	req.Header.Set("X-Goog-Api-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestVertexURLs(t *testing.T) {
	key := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}
	for _, c := range []struct{ base, want string }{
		{"", "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
		{"https://example.test", "https://example.test/v1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
		{"https://example.test/v1beta1", "https://example.test/v1beta1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
		{"https://example.test/v1/publishers/google/models", "https://example.test/v1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
	} {
		tg, err := (&vertexSettings{}).resolve(context.Background(), c.base, key, "gemini-x", method(true))
		if err != nil || tg.url != c.want {
			t.Errorf("API key, base %q: %q (%v), want %q", c.base, tg.url, err, c.want)
		}
	}
	for loc, host := range map[string]string{"": "https://aiplatform.googleapis.com", "global": "https://aiplatform.googleapis.com",
		"us": "https://aiplatform.us.rep.googleapis.com", "europe-west4": "https://europe-west4-aiplatform.googleapis.com"} {
		if got := vertexHost(loc); got != host {
			t.Errorf("vertexHost(%q) = %q, want %q", loc, got, host)
		}
	}
	if err := (&vertexSettings{Location: "us-central1/../x"}).check(); err == nil {
		t.Error("a location with a path in it was accepted")
	}
}

// TestServiceAccount sends requests from a service account: they go to its project's path with
// a bearer token, and concurrent requests share one token from the token endpoint.
func TestServiceAccount(t *testing.T) {
	srv := geminitest.New(t)
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	cred := &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}
	s, err := readSettings(settingsFor(srv, kindVertexSA))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tg, err := resolve(context.Background(), s, cred, "gemini-x", false)
			if err != nil {
				t.Error(err)
				return
			}
			req, _ := http.NewRequest(http.MethodPost, tg.url, strings.NewReader(`{"contents":[]}`))
			tg.authorize(req.Header)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	if n := srv.Grants(); n != 1 {
		t.Errorf("%d tokens fetched for 10 requests, want 1", n)
	}
	reqs := srv.Requests()
	if len(reqs) != 10 || reqs[0].Kind != "vertex" || reqs[0].Project != "conformance-project" || reqs[0].Location != "us-central1" {
		t.Fatalf("requests = %+v, want 10 to the settings' project in us-central1", reqs)
	}

	_, err = resolve(context.Background(), s, &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", false)}, "gemini-x", false)
	var e *llm.Error
	if !asError(err, &e) || e.Code != llm.CodeAuth || !e.Retryable || e.Scope != llm.ScopeCredential {
		t.Errorf("unknown service account: %v, want a retryable auth error", err)
	}
	srv.TokenDown = true
	_, err = resolve(context.Background(), s, &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-2", true)}, "gemini-x", false)
	if !asError(err, &e) || e.Code != llm.CodeUnavailable || !e.Retryable {
		t.Errorf("token endpoint down: %v, want a retryable unavailable error", err)
	}
}

func asError(err error, e **llm.Error) bool {
	le, ok := err.(*llm.Error)
	*e = le
	return ok
}
