package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// searchUpstream is a fake Codex search endpoint: it records the requests it gets and answers
// each with status and reply (a JSON object when status is 200).
type searchUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	seen   []searchRequest
}

type searchRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

func newSearchUpstream(t *testing.T) *searchUpstream {
	t.Helper()
	u := &searchUpstream{status: http.StatusOK}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.seen = append(u.seen, searchRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		status := u.status
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		if status == http.StatusOK {
			io.WriteString(w, `{"results":[{"title":"hit"}]}`)
			return
		}
		io.WriteString(w, `{"error":{"message":"refused","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *searchUpstream) requests() []searchRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]searchRequest(nil), u.seen...)
}

// searchCall runs an alpha search endpoint call on the built extension and returns its events.
func searchCall(t *testing.T, ext *host.Extension, acct protocol.Account, model, body string, headers map[string]string) []llm.Event {
	t.Helper()
	req := llm.Request{Model: model, Native: &llm.Native{Format: llm.FormatCodexAlphaSearch, Body: json.RawMessage(body), Headers: headers}}
	s := ext.Endpoint(t.Context(), acct, req)
	defer s.Close()
	var evs []llm.Event
	for {
		ev := s.Next()
		evs = append(evs, ev)
		if ev.Terminal() {
			return evs
		}
	}
}

func searchAccount(t *testing.T, u *searchUpstream, base string, cred *protocol.Credential, extra map[string]any) protocol.Account {
	t.Helper()
	settings := map[string]any{"base_url": u.URL + base}
	for k, v := range extra {
		settings[k] = v
	}
	raw, _ := json.Marshal(settings)
	return protocol.Account{ID: "codex", Settings: raw, Credential: cred}
}

// The search endpoint is served as today's core serves it: a subscription always, to
// {base_url}/alpha/search, an API key only with alpha_search on. The body goes on without the
// prompt cache hints, an API key's model renamed for the account; the headers are Codex's own
// originator and the client's version, user agent and request IDs; the reply comes back whole.
func TestAlphaSearch(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	client := map[string]string{"version": "0.154.0", "user-agent": "codex_cli_rs/0.154.0", "session_id": "s1", "x-client-request-id": "r1", "originator": "ignored"}
	apiKey := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-search"}
	body := `{"model":"alias","query":"q","prompt_cache_key":"k","prompt_cache_retention":"24h","id":"conv-1"}`

	// finished checks the reply: the upstream's body in one piece, under its content type.
	finished := func(t *testing.T, evs []llm.Event) {
		t.Helper()
		if len(evs) != 3 || evs[0].Type != llm.EventResponseStart || evs[0].Response.Format != llm.FormatCodexAlphaSearch ||
			string(evs[0].Response.Raw) != `{"content_type":"application/json; charset=utf-8"}` ||
			evs[1].Type != llm.EventNative || string(evs[1].Raw) != `"{\"results\":[{\"title\":\"hit\"}]}"` || evs[2].Type != llm.EventFinish {
			t.Fatalf("events = %+v", evs)
		}
	}

	t.Run("subscription", func(t *testing.T) {
		u := newSearchUpstream(t)
		evs := searchCall(t, ext, searchAccount(t, u, "/backend-api/codex", subscription("tok-sub", ""), nil), "up-model", body, client)
		finished(t, evs)
		reqs := u.requests()
		if len(reqs) != 1 || reqs[0].path != "/backend-api/codex/alpha/search" {
			t.Fatalf("requests = %+v", reqs)
		}
		h := reqs[0].header
		for name, want := range map[string]string{
			"Authorization": "Bearer tok-sub", "Chatgpt-Account-Id": "acct-codextest", "Originator": "codex_cli_rs",
			"Content-Type": "application/json", "Accept": "application/json", "Version": "0.154.0",
			"User-Agent": "codex_cli_rs/0.154.0", "Session_id": "s1", "X-Client-Request-Id": "r1",
		} {
			if got := h.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		// The model is renamed for the account, as for an API key (routing does it for every
		// endpoint; today's core leaves a subscription's body model as written).
		want := map[string]any{"model": "up-model", "query": "q", "id": "conv-1"}
		if !reflect.DeepEqual(reqs[0].body, want) {
			t.Errorf("body = %v, want %v", reqs[0].body, want)
		}
	})

	t.Run("API key with alpha_search", func(t *testing.T) {
		u := newSearchUpstream(t)
		evs := searchCall(t, ext, searchAccount(t, u, "/v1", apiKey, map[string]any{"alpha_search": true}), "up-model", body, client)
		finished(t, evs)
		reqs := u.requests()
		if len(reqs) != 1 || reqs[0].path != "/v1/alpha/search" {
			t.Fatalf("requests = %+v", reqs)
		}
		if got := reqs[0].header.Get("Authorization"); got != "Bearer sk-search" {
			t.Errorf("Authorization = %q", got)
		}
		if got := reqs[0].header.Get("Chatgpt-Account-Id"); got != "" {
			t.Errorf("Chatgpt-Account-Id = %q, want none for an API key", got)
		}
		if want := map[string]any{"model": "up-model", "query": "q", "id": "conv-1"}; !reflect.DeepEqual(reqs[0].body, want) {
			t.Errorf("body = %v, want %v (the model renamed for the account)", reqs[0].body, want)
		}
	})

	t.Run("API key without alpha_search", func(t *testing.T) {
		for name, extra := range map[string]map[string]any{"unset": nil, "off": {"alpha_search": false}} {
			u := newSearchUpstream(t)
			evs := searchCall(t, ext, searchAccount(t, u, "/v1", apiKey, extra), "up-model", body, client)
			e := evs[len(evs)-1].Error
			if len(evs) != 1 || e == nil || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
				t.Fatalf("%s: events = %+v, want unsupported on the credential, retryable", name, evs)
			}
			if n := len(u.requests()); n != 0 {
				t.Errorf("%s: the upstream saw %d requests, want none", name, n)
			}
		}
	})

	t.Run("no model", func(t *testing.T) {
		u := newSearchUpstream(t)
		evs := searchCall(t, ext, searchAccount(t, u, "/v1", apiKey, map[string]any{"alpha_search": true}), "", `{"query":"q"}`, nil)
		finished(t, evs)
		reqs := u.requests()
		if len(reqs) != 1 || !reflect.DeepEqual(reqs[0].body, map[string]any{"query": "q"}) {
			t.Fatalf("requests = %+v, want the body as it came", reqs)
		}
		if got := reqs[0].header.Get("User-Agent"); strings.Contains(got, "codex") {
			t.Errorf("User-Agent = %q, want none from the client (it sent none)", got)
		}
	})

	t.Run("upstream errors", func(t *testing.T) {
		for status, code := range map[int]string{http.StatusBadRequest: llm.CodeInvalidRequest, http.StatusUnauthorized: llm.CodeAuth, http.StatusTooManyRequests: llm.CodeRateLimit, http.StatusNotFound: llm.CodeNotFound} {
			u := newSearchUpstream(t)
			u.status = status
			evs := searchCall(t, ext, searchAccount(t, u, "/backend-api/codex", subscription("tok-sub", ""), nil), "up-model", body, client)
			e := evs[len(evs)-1].Error
			if len(evs) != 1 || e == nil || e.Code != code || e.Status != status || e.Message != "refused" {
				t.Errorf("status %d: events = %+v, want code %s", status, evs, code)
			}
			// A 404 means the upstream has no search endpoint: it must not become a block on the
			// model, which would stop the account generating with it.
			if status == http.StatusNotFound && (e.Scope != llm.ScopeRequest || e.Retryable) {
				t.Errorf("404: scope %q retryable %v, want a request-scoped error that blocks nothing", e.Scope, e.Retryable)
			}
		}
	})

	t.Run("bad body", func(t *testing.T) {
		u := newSearchUpstream(t)
		evs := searchCall(t, ext, searchAccount(t, u, "/backend-api/codex", subscription("tok-sub", ""), nil), "", `[1]`, nil)
		e := evs[len(evs)-1].Error
		if e == nil || e.Code != llm.CodeInvalidRequest || e.Scope != llm.ScopeRequest || len(u.requests()) != 0 {
			t.Errorf("events = %+v, want an invalid request refused before the upstream", evs)
		}
	})
}

// With no base_url, a subscription's search goes where today's core sends it.
func TestAlphaSearchDefaultURL(t *testing.T) {
	if got := baseURL(settings{}, account{subscription: true}) + "/alpha/search"; got != "https://chatgpt.com/backend-api/codex/alpha/search" {
		t.Errorf("subscription URL = %q", got)
	}
	if got := baseURL(settings{BaseURL: "https://example.invalid/v1/"}, account{}) + "/alpha/search"; got != "https://example.invalid/v1/alpha/search" {
		t.Errorf("API key URL = %q", got)
	}
}
