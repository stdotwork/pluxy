package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
)

// Fallback chains, as a person uses them: with every Claude account refusing, a request for a
// Claude model gets Claude's error; after pluxy chain set adds Codex as the next step, the same
// request is served by Codex, and the reply's headers say so; after pluxy chain remove it gets
// Claude's error again. Two providers serving one name never pool without a chain.
func TestChainServesFromTheNextStep(t *testing.T) {
	var claudeCalls atomic.Int32
	claude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages" && r.Method == http.MethodPost {
			claudeCalls.Add(1)
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your rate limit."}}`)
			return
		}
		io.WriteString(w, `{"data":[{"type":"model","id":"claude-opus-5-5"}],"has_more":false}`)
	}))
	t.Cleanup(claude.Close)
	codex := codextest.New(t)
	claudeAccount := func(id, key string) map[string]any {
		return map[string]any{"id": id, "models": []string{"claude-opus-5-5"}, "settings": map[string]any{"base_url": claude.URL},
			"credential": map[string]any{"kind": "api_key", "api_key": key}}
	}
	base, _, logs := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{claudeAccount("claude-a", "sk-ant-api-a"), claudeAccount("claude-b", "sk-ant-api-b")},
		},
		"extensions": map[string]any{"pluxy/codex": map[string]any{"accounts": []any{map[string]any{
			"id": "codex-a", "models": []string{"gpt-6.1-sol", "claude-opus-5-5"},
			"settings":   map[string]any{"base_url": codex.URL},
			"credential": map[string]any{"kind": "api_key", "api_key": codex.IssueToken("text")},
		}}}},
	}, "-bundled", bundledRegistry(t, map[string]string{"pluxy/codex": filepath.Join("codex", "codex")}))
	logs.waitFor(t, "config revision 1 active")
	env := []string{"PLUXY_URL=" + base, "PLUXY_MANAGEMENT_KEY=" + managementKey}

	const body = `{"model":"claude-opus-5-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	ask := func() (*http.Response, explainBody) {
		t.Helper()
		resp := postPath(t, context.Background(), base, "/v1/messages", clientKey, body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var b explainBody
		_ = json.Unmarshal(raw, &b)
		return resp, b
	}
	claudeError := func(when string) {
		t.Helper()
		resp, b := ask()
		if resp.StatusCode != http.StatusTooManyRequests || b.Error.Pluxy == nil {
			t.Fatalf("%s: status %d, pluxy %+v; want Claude's 429", when, resp.StatusCode, b.Error.Pluxy)
		}
		if p := resp.Header.Get("X-Pluxy-Provider"); p != "" && p != "pluxy/claude" {
			t.Errorf("%s: X-Pluxy-Provider = %q", when, p)
		}
		for _, x := range b.Error.Pluxy.Tried {
			if x.Provider != "pluxy/claude" {
				t.Errorf("%s: tried %+v, an account of another provider", when, x)
			}
		}
		if n := len(codex.Requests()); n != 0 {
			t.Errorf("%s: Codex got %d requests; codex-a also lists claude-opus-5-5, but Claude's accounts come first in the config and two providers do not pool", when, n)
		}
	}

	// No chain: the request stays on Claude, whose accounts both refuse.
	claudeError("no chain")
	if n := claudeCalls.Load(); n != 2 {
		t.Errorf("Claude got %d requests, want one per account", n)
	}

	r := runCLI(t, env, "chain", "set", "claude-opus-5-5", "claude-opus-5-5@pluxy/claude", "gpt-6.1-sol")
	if r.code != 0 {
		t.Fatalf("chain set: %s", r)
	}
	r = runCLI(t, env, "chain", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "claude-opus-5-5@pluxy/claude") || !strings.Contains(r.stdout, "gpt-6.1-sol") {
		t.Errorf("chain list: %s", r)
	}

	// The chain: Claude's accounts are blocked, so the request goes to step 2 at once.
	resp, b := ask()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with the chain: status %d, %+v", resp.StatusCode, b)
	}
	for h, want := range map[string]string{"X-Pluxy-Provider": "pluxy/codex", "X-Pluxy-Model": "gpt-6.1-sol"} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("with the chain: %s = %q, want %q", h, got, want)
		}
	}
	if n := claudeCalls.Load(); n != 2 {
		t.Errorf("Claude got %d requests, want still 2: its blocked accounts are not tried again", n)
	}
	// The usage record names the step that served.
	code, raw := mcall(t, base, managementKey, http.MethodGet, "/usage/events", nil)
	var events struct {
		Events []struct {
			Model, Provider string
			ChainStep       int `json:"chain_step"`
		} `json:"events"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &events) != nil || len(events.Events) == 0 {
		t.Fatalf("GET /usage/events: %d %s", code, raw)
	}
	if e := events.Events[len(events.Events)-1]; e.ChainStep != 2 || e.Provider != "pluxy/codex" || e.Model != "claude-opus-5-5" {
		t.Errorf("last usage event %+v, want chain_step 2 on pluxy/codex for claude-opus-5-5", e)
	}
	// pluxy route shows both steps.
	r = runCLI(t, env, "route", "claude-opus-5-5")
	if r.code != 0 || !strings.Contains(r.stdout, "pluxy/codex") || !strings.Contains(r.stdout, "cooling") {
		t.Errorf("route: %s", r)
	}

	// What core refuses: more than 8 steps, a step no account serves, a step naming a chain.
	nine := []string{"chain", "set", "too-long"}
	for range 9 {
		nine = append(nine, "gpt-6.1-sol")
	}
	for _, args := range [][]string{nine, {"chain", "set", "x", "no-such-model"}, {"chain", "set", "x", "gpt-6.1-sol@pluxy/claude"},
		{"chain", "set", "x", "claude-opus-5-5"}} {
		if r := runCLI(t, env, args...); r.code != 6 {
			t.Errorf("%q: %s; want exit 6 (core refused it)", args, r)
		}
	}

	if r := runCLI(t, env, "chain", "remove", "claude-opus-5-5"); r.code != 0 {
		t.Fatalf("chain remove: %s", r)
	}
	if r := runCLI(t, env, "chain", "remove", "claude-opus-5-5"); r.code != 5 {
		t.Errorf("removing a chain that is not there: %s; want exit 5", r)
	}
	// Without the chain, the request gets Claude's error again.
	codexBefore := len(codex.Requests())
	resp, b = ask()
	if resp.StatusCode != http.StatusTooManyRequests || len(codex.Requests()) != codexBefore {
		t.Errorf("after chain remove: status %d, Codex requests %d then %d; want Claude's 429 and none", resp.StatusCode, codexBefore, len(codex.Requests()))
	}
}
