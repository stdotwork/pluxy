package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
)

// startClaudeCore runs core with the Claude extension, no configured accounts and the management
// API on.
func startClaudeCore(t *testing.T) string {
	t.Helper()
	return runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
}

// subscriptionLogin adds account id by signing in to the fake Claude in a browser and pasting
// the address the browser was sent to.
func subscriptionLogin(t *testing.T, base string, srv *claudetest.Server, id string) {
	t.Helper()
	var l struct {
		Login struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Prompt *struct {
				Kind string `json:"kind"`
				URL  string `json:"url"`
			} `json:"prompt"`
			Error *struct{ Code, Message string } `json:"error"`
		} `json:"login"`
	}
	if code := manage(t, base, "POST", "/accounts", map[string]any{
		"id": id, "method": "subscription", "settings": srv.Settings(""),
	}, &l); code != http.StatusAccepted || l.Login.Prompt == nil || l.Login.Prompt.Kind != "browser" || l.Login.Prompt.URL == "" {
		t.Fatalf("starting the login: status %d, %+v", code, l.Login)
	}
	redirect := claudetest.SignIn(t, l.Login.Prompt.URL)
	if code := manage(t, base, "POST", "/logins/"+l.Login.ID+"/input", map[string]any{
		"input": map[string]string{"redirect_url": redirect},
	}, nil); code/100 != 2 {
		t.Fatalf("sending the redirect address: status %d", code)
	}
	deadline := time.Now().Add(15 * time.Second)
	for l.Login.State == "pending" {
		if time.Now().After(deadline) {
			t.Fatal("the login did not finish")
		}
		time.Sleep(50 * time.Millisecond)
		manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &l)
	}
	if l.Login.State != "done" {
		t.Fatalf("login ended %s: %+v", l.Login.State, l.Login.Error)
	}
}

// claudeCodeBody is a Messages request as Claude Code sends one: a thinking block with its
// signature replayed in a tool loop and a cache_control marker.
const claudeCodeBody = `{"model":"claude-opus-4-6","max_tokens":1024,` +
	`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}],` +
	`"messages":[{"role":"user","content":"List the files."},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":"I should call ls.","signature":"EqoBCkgIARABGAIiQL7example"},` +
	`{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a.txt"}]}]}`

// postClaudeCode sends claudeCodeBody to /v1/messages with Claude Code's headers.
func postClaudeCode(t *testing.T, base string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(claudeCodeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", clientKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14")
	req.Header.Set("User-Agent", "claude-cli/2.1.0 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("X-Stainless-Lang", "js")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The slice's check for subscriptions: an account added by Claude subscription login serves a
// Claude Code request unchanged (body, thinking signature, cache marker and the client's own
// headers) with its OAuth token; a Chat Completions client on the same account is translated,
// with its reasoning effort as thinking and nothing presenting it as Claude Code; and when the
// access token stops working, concurrent requests share one renewal and all succeed.
func TestClaudeSubscription(t *testing.T) {
	srv := claudetest.New(t)
	base := startClaudeCore(t)
	subscriptionLogin(t, base, srv, "max")

	if code, body := postClaudeCode(t, base); code != http.StatusOK {
		t.Fatalf("Claude Code request: status %d: %s", code, body)
	}
	got := srv.Requests()[0]
	if !bytes.Equal(got.Body, []byte(claudeCodeBody)) {
		t.Errorf("upstream body changed:\n got %s\nwant %s", got.Body, claudeCodeBody)
	}
	for h, want := range map[string]string{
		"Anthropic-Version": "2023-06-01",
		"Anthropic-Beta":    "claude-code-20250219,interleaved-thinking-2025-05-14,oauth-2025-04-20",
		"User-Agent":        "claude-cli/2.1.0 (external, cli)",
		"X-App":             "cli",
		"X-Stainless-Lang":  "js",
		"X-Api-Key":         "",
	} {
		if v := got.Header.Get(h); v != want {
			t.Errorf("upstream %s = %q, want %q", h, v, want)
		}
	}
	access, ok := strings.CutPrefix(got.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(access, "claudetest-access-") {
		t.Fatalf("upstream Authorization = %q, want the subscription's access token", got.Header.Get("Authorization"))
	}

	resp := post(t, context.Background(), base, clientKey,
		`{"model":"claude-opus-4-6","reasoning_effort":"high","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Hi"}]}`)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "Hello") {
		t.Fatalf("Chat request: status %d: %s", resp.StatusCode, b)
	}
	chat := srv.Requests()[1]
	var sent struct {
		System       []struct{ Text string } `json:"system"`
		Thinking     map[string]any          `json:"thinking"`
		OutputConfig map[string]any          `json:"output_config"`
	}
	if err := json.Unmarshal(chat.Body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.System) != 1 || sent.System[0].Text != "Be brief." {
		t.Errorf("translated system = %+v, want only the client's own", sent.System)
	}
	if sent.Thinking["type"] != "adaptive" || sent.OutputConfig["effort"] != "high" {
		t.Errorf("translated thinking = %v, output_config = %v; want adaptive, effort high", sent.Thinking, sent.OutputConfig)
	}
	if ua, app := chat.Header.Get("User-Agent"), chat.Header.Get("X-App"); strings.HasPrefix(ua, "claude-cli") || app != "" {
		t.Errorf("a Chat client went upstream as User-Agent %q, X-App %q", ua, app)
	}
	if v := chat.Header.Get("Anthropic-Beta"); v != "oauth-2025-04-20" {
		t.Errorf("translated request Anthropic-Beta = %q, want only the OAuth beta", v)
	}

	// The access token stops working: every request is refused once, core renews the
	// credential once (refresh tokens rotate, so a second renewal would fail) and retries.
	srv.Revoke(access)
	const n = 20
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = postClaudeCode(t, base)
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d after the token stopped working: status %d", i, c)
		}
	}
	if r := srv.Renewals(); r != 1 {
		t.Errorf("renewals = %d, want 1", r)
	}
}

// An API key account sends the key in x-api-key, never as a bearer token, and adds no OAuth beta.
func TestClaudeAPIKey(t *testing.T) {
	srv := claudetest.New(t)
	key := srv.IssueKey()
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "key", "settings": srv.Settings(""),
				"credential": map[string]any{"kind": "api_key", "api_key": key},
			}},
		},
	})
	if code, body := postClaudeCode(t, base); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	got := srv.Requests()[0]
	if got.Header.Get("X-Api-Key") != key || got.Header.Get("Authorization") != "" {
		t.Errorf("upstream X-Api-Key = %q, Authorization = %q; want the key in x-api-key only", got.Header.Get("X-Api-Key"), got.Header.Get("Authorization"))
	}
	if v := got.Header.Get("Anthropic-Beta"); v != "claude-code-20250219,interleaved-thinking-2025-05-14" {
		t.Errorf("upstream Anthropic-Beta = %q, want the client's", v)
	}
}
