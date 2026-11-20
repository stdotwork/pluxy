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

// The Codex CLI reads its model catalog and usage through the ChatGPT backend. Core passes
// allowlisted backend calls to the account with its ChatGPT token, but still answers a path the
// CLI does not call itself with 404 without contacting the upstream.
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
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
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
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	models := do(http.MethodGet, "/backend-api/codex/models?client_version=0.154.0", "")
	modelsBody, _ := io.ReadAll(models.Body)
	models.Body.Close()
	if models.StatusCode != http.StatusOK || string(modelsBody) != `{"from":"backend"}` {
		t.Fatalf("GET models: status %d body %s", models.StatusCode, modelsBody)
	}

	query := `{"threads":[{"thread_id":"thread-1","descendant_thread_ids":[]}]}`
	usage := do(http.MethodPost, "/backend-api/wham/usage/thread_usage/query", query)
	usageBody, _ := io.ReadAll(usage.Body)
	usage.Body.Close()
	if usage.StatusCode != http.StatusOK || string(usageBody) != `{"from":"backend"}` {
		t.Fatalf("POST thread usage: status %d body %s", usage.StatusCode, usageBody)
	}

	unknown := do(http.MethodGet, "/backend-api/codex/not-called", "")
	_, _ = io.Copy(io.Discard, unknown.Body)
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown backend path: status %d, want 404", unknown.StatusCode)
	}

	mu.Lock()
	got := append([]codexBackendRequest(nil), seen...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("upstream got %d requests, want the two allowlisted calls: %+v", len(got), got)
	}
	if got[0].method != http.MethodGet || got[0].uri != "/backend-api/codex/models?client_version=0.154.0" {
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
		if v := r.header.Get("X-Pluxy-Codex-Backend-Path"); v != "" {
			t.Errorf("%s %s leaked the internal path header %q", r.method, r.uri, v)
		}
		var body map[string]any
		if json.Unmarshal(r.body, &body) == nil && body["client_key"] == clientKey {
			t.Errorf("%s %s sent the client key upstream", r.method, r.uri)
		}
	}
}
