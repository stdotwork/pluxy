package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type backendSeen struct {
	method, uri string
	header      http.Header
	body        []byte
}

// A codex.backend call goes to the ChatGPT backend root (the account's base_url without /codex)
// with the call's method, escaped path, query and body kept as they came, the account's token and
// ChatGPT account and client identity, and the client's version, request IDs and FedRAMP header; the reply comes back whole.
// An API key, which has no such backend, and a path outside the root send nothing.
func TestBackend(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)

	var mu sync.Mutex
	var seen []backendSeen
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, backendSeen{r.Method, r.RequestURI, r.Header.Clone(), body})
		st := status
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		if st == http.StatusOK {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"detail":"Not Found"}`)
	}))
	t.Cleanup(srv.Close)
	requests := func() []backendSeen {
		mu.Lock()
		defer mu.Unlock()
		return append([]backendSeen(nil), seen...)
	}
	account := func(cred *protocol.Credential) protocol.Account {
		raw, _ := json.Marshal(map[string]any{"base_url": srv.URL + "/backend-api/codex"})
		return protocol.Account{ID: "codex", Settings: raw, Credential: cred}
	}
	call := func(acct protocol.Account, c map[string]any) []llm.Event {
		body, _ := json.Marshal(c)
		headers := map[string]string{"version": "0.154.0", "user-agent": "codex_cli_rs/0.154.0", "session_id": "s1", "x-client-request-id": "r1", "x-openai-fedramp": "true"}
		s := ext.Endpoint(t.Context(), acct, llm.Request{Native: &llm.Native{Format: llm.FormatCodexBackend, Body: body, Headers: headers}})
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
	sub := account(subscription("tok-sub", ""))

	t.Run("GET with a query", func(t *testing.T) {
		evs := call(sub, map[string]any{"method": "GET", "path": "/wham/usage/plan_limit_history", "query": "days=7"})
		if len(evs) != 3 || evs[0].Response.Format != llm.FormatCodexBackend || string(evs[1].Raw) != `"{\"ok\":true}"` || evs[2].Type != llm.EventFinish {
			t.Fatalf("events = %+v", evs)
		}
		got := requests()
		if len(got) != 1 || got[0].method != "GET" || got[0].uri != "/backend-api/wham/usage/plan_limit_history?days=7" || len(got[0].body) != 0 {
			t.Fatalf("requests = %+v", got)
		}
		h := got[0].header
		for name, want := range map[string]string{
			"Authorization": "Bearer tok-sub", "Chatgpt-Account-Id": "acct-codextest", "Originator": "codex-tui", // the account's client identity, as on /responses
			"Accept": "application/json", "Version": "0.154.0", "Session_id": "s1", "X-Client-Request-Id": "r1",
			// A FedRAMP account's CLI sends this on every backend call (codex-rs
			// backend-client/src/client.rs), so it reaches the backend as it came.
			"X-Openai-Fedramp": "true",
		} {
			if v := h.Get(name); v != want {
				t.Errorf("%s = %q, want %q", name, v, want)
			}
		}
		if v := h.Get("Content-Type"); v != "" {
			t.Errorf("a GET sent Content-Type %q", v)
		}
	})

	t.Run("a body that is not JSON and an escaped path", func(t *testing.T) {
		before := len(requests())
		audio := []byte("--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\n\x00\xff\r\n--b--\r\n")
		evs := call(sub, map[string]any{"method": "POST", "path": "/wham/tasks/a%20b", "content_type": "multipart/form-data; boundary=b", "body": audio})
		if last := evs[len(evs)-1]; last.Type != llm.EventFinish {
			t.Fatalf("events = %+v", evs)
		}
		got := requests()[before:]
		if len(got) != 1 || got[0].uri != "/backend-api/wham/tasks/a%20b" || string(got[0].body) != string(audio) || got[0].header.Get("Content-Type") != "multipart/form-data; boundary=b" {
			t.Fatalf("requests = %+v", got)
		}
	})

	t.Run("an API key", func(t *testing.T) {
		before := len(requests())
		cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-backend"}
		evs := call(account(cred), map[string]any{"method": "GET", "path": "/wham/usage"})
		last := evs[len(evs)-1]
		if last.Type != llm.EventError || last.Error.Code != llm.CodeUnsupported || last.Error.Scope != llm.ScopeCredential {
			t.Fatalf("events = %+v", evs)
		}
		if n := len(requests()); n != before {
			t.Errorf("an API key's call sent %d requests upstream", n-before)
		}
	})

	t.Run("calls outside the root", func(t *testing.T) {
		before := len(requests())
		for _, c := range []map[string]any{
			{"method": "GET", "path": "/wham/../codex/responses"},
			{"method": "GET", "path": "/wham/%2e%2e/x"},
			{"method": "GET", "path": "/wham/a%2Fb"},
			{"method": "GET", "path": "wham/usage"},
			{"method": "GET", "path": "/wham//usage"},
			{"method": "GET", "path": "/wham/usage/"},
			{"method": "PATCH", "path": "/wham/usage"},
			{"method": "GET"},
		} {
			evs := call(sub, c)
			last := evs[len(evs)-1]
			if last.Type != llm.EventError || last.Error.Code != llm.CodeInvalidRequest || last.Error.Scope != llm.ScopeRequest {
				t.Errorf("call %v: events = %+v", c, evs)
			}
		}
		if n := len(requests()); n != before {
			t.Errorf("refused calls sent %d requests upstream", n-before)
		}
	})

	t.Run("a backend that has no such endpoint", func(t *testing.T) {
		mu.Lock()
		status = http.StatusNotFound
		mu.Unlock()
		evs := call(sub, map[string]any{"method": "GET", "path": "/wham/usage"})
		last := evs[len(evs)-1]
		// The account is not at fault for a missing endpoint: the error is the request's.
		if last.Type != llm.EventError || last.Error.Code != llm.CodeNotFound || last.Error.Scope != llm.ScopeRequest {
			t.Fatalf("events = %+v", evs)
		}
	})

	// A backend call's refusal or rate limit is about the call, not the account's ability to
	// generate: no status cools the credential or a model, but a 401 is the token refused.
	t.Run("an upstream error keeps off generation", func(t *testing.T) {
		for _, c := range []struct {
			status int
			scope  string
			code   string
		}{
			{http.StatusForbidden, llm.ScopeRequest, llm.CodePermission},
			{http.StatusTooManyRequests, llm.ScopeRequest, llm.CodeRateLimit},
			{http.StatusUnauthorized, llm.ScopeCredential, llm.CodeAuth},
		} {
			mu.Lock()
			status = c.status
			mu.Unlock()
			evs := call(sub, map[string]any{"method": "GET", "path": "/wham/usage"})
			last := evs[len(evs)-1]
			if last.Type != llm.EventError || last.Error.Scope != c.scope || last.Error.Code != c.code {
				t.Errorf("status %d: events = %+v", c.status, evs)
			}
		}
		mu.Lock()
		status = http.StatusOK
		mu.Unlock()
	})
}
