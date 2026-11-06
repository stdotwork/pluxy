package e2e

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
)

// A ChatGPT subscription added by device login through the management API serves Codex
// requests with its access token and ChatGPT account; when the token is about to expire, many
// concurrent requests share one refresh (the fake login server rotates refresh tokens and refuses
// any reuse), and all of them are served with the renewed token.
func TestCodexSubscriptionLoginAndConcurrentRefresh(t *testing.T) {
	srv := codextest.New(t)
	// The first token expires in a second, inside core's renewal lead; renewed ones last.
	srv.AccessTTL = func(n int) time.Duration {
		if n == 1 {
			return time.Second
		}
		return time.Hour
	}
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	settings := map[string]any{"base_url": srv.URL + "/backend-api/codex", "auth_base_url": srv.URL}
	var l loginReply
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "chatgpt", "method": "device", "settings": settings}, &l); code != http.StatusAccepted ||
		l.Login.Prompt == nil || l.Login.Prompt.UserCode == "" {
		t.Fatalf("starting the login: status %d, %+v", code, l.Login)
	}
	srv.ApproveDevice(l.Login.Prompt.UserCode)
	deadline := time.Now().Add(15 * time.Second)
	for l.Login.State == "pending" {
		if time.Now().After(deadline) {
			t.Fatal("the login did not finish")
		}
		time.Sleep(100 * time.Millisecond)
		manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &l)
	}
	if l.Login.State != "done" {
		t.Fatalf("login ended %s: %+v", l.Login.State, l.Login.Error)
	}

	release := srv.BlockRefresh()
	const n = 10
	var wg sync.WaitGroup
	status := make([]int, n)
	text := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, `{"model":"gpt-6.1-sol","input":"Say hello"}`)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			status[i], text[i] = resp.StatusCode, string(b)
		}()
	}
	select {
	case <-srv.RefreshStarted():
	case <-time.After(10 * time.Second):
		t.Fatal("no refresh reached the login server")
	}
	time.Sleep(300 * time.Millisecond) // every request is now waiting on the one refresh
	release()
	wg.Wait()
	for i := range n {
		if status[i] != http.StatusOK || !strings.Contains(text[i], "Hello from the conformance kit.") {
			t.Errorf("request %d: status %d, body %s", i, status[i], text[i])
		}
	}
	if c := srv.RefreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
	reqs := srv.Requests()
	if len(reqs) != n {
		t.Fatalf("%d model requests reached the upstream, want %d", len(reqs), n)
	}
	for _, r := range reqs {
		if r.Path != "/backend-api/codex/responses" {
			t.Errorf("request went to %s", r.Path)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != codextest.AccountID {
			t.Errorf("Chatgpt-Account-Id = %q, want %q", got, codextest.AccountID)
		}
		if r.Header.Get("Authorization") != reqs[0].Header.Get("Authorization") {
			t.Fatalf("requests used different tokens")
		}
		if stream, _ := r.Body["stream"].(bool); !stream {
			t.Errorf("a request was not streamed upstream: %v", r.Body)
		}
	}
}
