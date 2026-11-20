package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
)

type codexBackendRequest struct {
	method, uri string
	header      http.Header
	body        []byte
}

// The Codex CLI reads a ChatGPT account's usage through its chatgpt_base_url (the /backend-api
// root): the plan limit history is GET wham/usage/plan_limit_history?days=7 and the thread
// usage is POST wham/usage/thread_usage/query (codex-rs/backend-client). Core passes such an
// allowlisted call to the account's ChatGPT backend with the account's token and the client's
// FedRAMP header, and answers a path the CLI does not call with 404 without contacting the
// upstream.
func TestCodexBackendRoutes(t *testing.T) {
	srv := codextest.New(t)
	var mu sync.Mutex
	var seen []codexBackendRequest
	srv.Backend = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, codexBackendRequest{r.Method, r.URL.RequestURI(), r.Header.Clone(), body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"from":"backend"}`))
	})

	access, _, idToken := srv.Grant()
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "chatgpt",
				"settings":   map[string]any{"base_url": srv.URL + "/backend-api/codex"},
				"credential": map[string]any{"kind": "oauth", "access_token": access, "data": map[string]any{"id_token": idToken}},
			}},
		},
	})

	do := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+clientKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "codex_cli_rs/0.154.0")
		req.Header.Set("Version", "0.154.0")
		req.Header.Set("Session_id", "backend-session")
		req.Header.Set("X-OpenAI-Fedramp", "true")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	history := do(http.MethodGet, "/backend-api/wham/usage/plan_limit_history?days=7", "")
	historyBody, _ := io.ReadAll(history.Body)
	history.Body.Close()
	if history.StatusCode != http.StatusOK || string(historyBody) != `{"from":"backend"}` {
		t.Fatalf("GET plan limit history: status %d body %s", history.StatusCode, historyBody)
	}

	query := `{"thread_ids":["thread-1"]}`
	usage := do(http.MethodPost, "/backend-api/wham/usage/thread_usage/query", query)
	usageBody, _ := io.ReadAll(usage.Body)
	usage.Body.Close()
	if usage.StatusCode != http.StatusOK || string(usageBody) != `{"from":"backend"}` {
		t.Fatalf("POST thread usage: status %d body %s", usage.StatusCode, usageBody)
	}

	// Neither a path the CLI does not call, nor one below an allowlisted path, goes upstream.
	for _, path := range []string{"/backend-api/codex/not-called", "/backend-api/wham/usage/thread_usage/query/extra", "/backend-api/wham/../codex/not-called"} {
		unknown := do(http.MethodPost, path, "{}")
		_, _ = io.Copy(io.Discard, unknown.Body)
		unknown.Body.Close()
		if unknown.StatusCode != http.StatusNotFound {
			t.Fatalf("POST %s: status %d, want 404", path, unknown.StatusCode)
		}
	}
	// The mux matches the escaped path, so this one reaches the allowlist's own check as well: a
	// segment that unescapes to a slash is refused there, not passed on as another path.
	escaped := do(http.MethodGet, "/backend-api/wham/tasks/a%2Fb", "")
	_, _ = io.Copy(io.Discard, escaped.Body)
	escaped.Body.Close()
	if escaped.StatusCode != http.StatusNotFound {
		t.Fatalf("GET a%%2Fb task: status %d, want 404", escaped.StatusCode)
	}

	mu.Lock()
	got := append([]codexBackendRequest(nil), seen...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("upstream got %d requests, want the two allowlisted calls: %+v", len(got), got)
	}
	if got[0].method != http.MethodGet || got[0].uri != "/backend-api/wham/usage/plan_limit_history?days=7" {
		t.Errorf("first upstream call = %s %s", got[0].method, got[0].uri)
	}
	if got[1].method != http.MethodPost || got[1].uri != "/backend-api/wham/usage/thread_usage/query" {
		t.Errorf("second upstream call = %s %s", got[1].method, got[1].uri)
	}
	if string(got[1].body) != query {
		t.Errorf("upstream body = %s, want %s", got[1].body, query)
	}
	for _, r := range got {
		if auth := r.header.Get("Authorization"); auth != "Bearer "+access {
			t.Errorf("%s %s Authorization = %q, want the account token", r.method, r.uri, auth)
		}
		if id := r.header.Get("Chatgpt-Account-Id"); id != codextest.AccountID {
			t.Errorf("%s %s Chatgpt-Account-Id = %q", r.method, r.uri, id)
		}
		// A FedRAMP account's CLI sends this on every backend call; it reaches the backend as sent.
		if v := r.header.Get("X-Openai-Fedramp"); v != "true" {
			t.Errorf("%s %s X-OpenAI-Fedramp = %q, want the client's", r.method, r.uri, v)
		}
		for name := range r.header {
			if strings.HasPrefix(strings.ToLower(name), "x-pluxy") {
				t.Errorf("%s %s sent the internal header %q upstream", r.method, r.uri, name)
			}
		}
		if r.method == http.MethodGet && len(r.body) != 0 {
			t.Errorf("%s %s sent a body upstream: %s", r.method, r.uri, r.body)
		}
		var body map[string]any
		if json.Unmarshal(r.body, &body) == nil && body["client_key"] == clientKey {
			t.Errorf("%s %s sent the client key upstream", r.method, r.uri)
		}
	}
}
