package e2e

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
)

// A client's service tier, whatever its format, reaches Claude as the tier Messages has for it, and
// only a priority tier (priority or fast) becomes fast mode, with the beta Anthropic requires. The
// old core records no such translations, so this is checked here, at the HTTP seam: core serves the
// client, the Claude extension sends the request, and the fake Anthropic API records it.
func TestServiceTierToClaude(t *testing.T) {
	srv := claudetest.New(t)
	base := startClaudeCore(t)
	subscriptionLogin(t, base, srv, "max")

	for _, c := range []struct {
		name, path, body string
		speed, tier      string // the upstream body's speed and service_tier; "" for none
		fastBeta         bool
	}{
		{"chat priority", "/v1/chat/completions", `{"model":"claude-opus-4-6","service_tier":"priority","messages":[{"role":"user","content":"Hi"}]}`, "fast", "", true},
		{"chat fast", "/v1/chat/completions", `{"model":"claude-opus-4-6","service_tier":"fast","messages":[{"role":"user","content":"Hi"}]}`, "fast", "", true},
		{"responses priority", "/v1/responses", `{"model":"claude-opus-4-6","service_tier":"priority","input":"Hi"}`, "fast", "", true},
		{"gemini priority", "/v1beta/models/claude-opus-4-6:generateContent", `{"serviceTier":"priority","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`, "fast", "", true},
		{"chat auto", "/v1/chat/completions", `{"model":"claude-opus-4-6","service_tier":"auto","messages":[{"role":"user","content":"Hi"}]}`, "", "auto", false},
		{"chat default", "/v1/chat/completions", `{"model":"claude-opus-4-6","service_tier":"default","messages":[{"role":"user","content":"Hi"}]}`, "", "standard_only", false},
		{"chat flex", "/v1/chat/completions", `{"model":"claude-opus-4-6","service_tier":"flex","messages":[{"role":"user","content":"Hi"}]}`, "", "", false},
		{"chat scale", "/v1/chat/completions", `{"model":"claude-opus-4-6","service_tier":"scale","messages":[{"role":"user","content":"Hi"}]}`, "", "", false},
		{"chat ultrafast", "/v1/chat/completions", `{"model":"claude-opus-4-6","service_tier":"ultrafast","messages":[{"role":"user","content":"Hi"}]}`, "", "", false},
		{"chat none", "/v1/chat/completions", `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"Hi"}]}`, "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(srv.Requests())
			resp := postPath(t, context.Background(), base, c.path, clientKey, c.body)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, b)
			}
			reqs := srv.Requests()
			if len(reqs) != before+1 {
				t.Fatalf("upstream got %d requests, want 1", len(reqs)-before)
			}
			got := reqs[before]
			var sent struct {
				Speed       string `json:"speed"`
				ServiceTier string `json:"service_tier"`
			}
			if err := json.Unmarshal(got.Body, &sent); err != nil {
				t.Fatal(err)
			}
			if sent.Speed != c.speed || sent.ServiceTier != c.tier {
				t.Errorf("upstream speed %q and service_tier %q, want %q and %q: %s", sent.Speed, sent.ServiceTier, c.speed, c.tier, got.Body)
			}
			if beta := strings.Contains(got.Header.Get("Anthropic-Beta"), "fast-mode-2026-02-01"); beta != c.fastBeta {
				t.Errorf("Anthropic-Beta = %q, fast-mode beta present: %v, want %v", got.Header.Get("Anthropic-Beta"), beta, c.fastBeta)
			}
		})
	}
}

// A client's service tier reaches Codex as the Responses tier it means, whatever the client's
// format: priority and fast (a Messages client's fast speed too) as priority, with the
// subscription's routing hint naming it; ultrafast as it is; and the tiers Codex does not take
// (auto, default, flex, scale) are left off.
func TestServiceTierToCodex(t *testing.T) {
	srv := codextest.New(t)
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

	for _, c := range []struct {
		name, path, body string
		tier             string // service_tier of the upstream body; "" for none
	}{
		{"chat priority", "/v1/chat/completions", `{"model":"gpt-6.1-sol","service_tier":"priority","messages":[{"role":"user","content":"Hi"}]}`, "priority"},
		{"chat fast", "/v1/chat/completions", `{"model":"gpt-6.1-sol","service_tier":"fast","messages":[{"role":"user","content":"Hi"}]}`, "priority"},
		{"chat ultrafast", "/v1/chat/completions", `{"model":"gpt-6.1-sol","service_tier":"ultrafast","messages":[{"role":"user","content":"Hi"}]}`, "ultrafast"},
		{"chat flex", "/v1/chat/completions", `{"model":"gpt-6.1-sol","service_tier":"flex","messages":[{"role":"user","content":"Hi"}]}`, ""},
		{"chat default", "/v1/chat/completions", `{"model":"gpt-6.1-sol","service_tier":"default","messages":[{"role":"user","content":"Hi"}]}`, ""},
		{"messages speed fast", "/v1/messages", `{"model":"gpt-6.1-sol","max_tokens":50,"speed":"fast","messages":[{"role":"user","content":"Hi"}]}`, "priority"},
		{"messages standard_only", "/v1/messages", `{"model":"gpt-6.1-sol","max_tokens":50,"service_tier":"standard_only","messages":[{"role":"user","content":"Hi"}]}`, ""},
		{"gemini priority", "/v1beta/models/gpt-6.1-sol:generateContent", `{"serviceTier":"priority","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`, "priority"},
		{"chat none", "/v1/chat/completions", `{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"Hi"}]}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(srv.Requests())
			headers := map[string]string{}
			if strings.HasPrefix(c.path, "/v1/messages") {
				headers["Anthropic-Version"] = "2023-06-01"
			}
			req, _ := http.NewRequest(http.MethodPost, base+c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+clientKey)
			req.Header.Set("X-Api-Key", clientKey)
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, b)
			}
			reqs := srv.Requests()
			if len(reqs) != before+1 {
				t.Fatalf("upstream got %d requests, want 1", len(reqs)-before)
			}
			got := reqs[before]
			tier, _ := got.Body["service_tier"].(string)
			if tier != c.tier {
				t.Errorf("upstream service_tier = %q, want %q: %v", tier, c.tier, got.Body)
			}
			want := "model=gpt-6.1-sol"
			if c.tier != "" {
				want += ";tier=" + c.tier
			}
			if hint := got.Header.Get("X-Codex-Routing-Hint"); hint != want {
				t.Errorf("X-Codex-Routing-Hint = %q, want %q", hint, want)
			}
		})
	}
}

// wireReplies are the smallest replies each upstream wire accepts.
var wireReplies = map[string]string{
	"chat":      `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`,
	"responses": `{"id":"r1","object":"response","created_at":1,"status":"completed","model":"m","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done."}]}]}`,
	"messages":  `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
	"gemini":    `{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
}

// A client's service tier and verbosity reach an OpenAI-compatible upstream in the form its wire
// has for them, and what the wire cannot say is dropped, not moved to another setting: the request
// still goes (the default drop policy), without it. The old core records no such translations, so
// this is checked at the HTTP seam, with a fake upstream per wire.
func TestServiceTierAcrossWires(t *testing.T) {
	const prompt = `[{"role":"user","content":"Hi"}]`
	clients := map[string]struct{ path, body string }{
		"chat-priority":      {"/v1/chat/completions", `{"model":"m","service_tier":"priority","verbosity":"low","messages":` + prompt + `}`},
		"chat-fast":          {"/v1/chat/completions", `{"model":"m","service_tier":"fast","messages":` + prompt + `}`},
		"chat-flex":          {"/v1/chat/completions", `{"model":"m","service_tier":"flex","messages":` + prompt + `}`},
		"chat-auto":          {"/v1/chat/completions", `{"model":"m","service_tier":"auto","messages":` + prompt + `}`},
		"responses-priority": {"/v1/responses", `{"model":"m","service_tier":"priority","text":{"verbosity":"high"},"input":"Hi"}`},
		"messages-fast":      {"/v1/messages", `{"model":"m","max_tokens":9,"speed":"fast","messages":` + prompt + `}`},
		"messages-auto":      {"/v1/messages", `{"model":"m","max_tokens":9,"service_tier":"auto","messages":` + prompt + `}`},
		"gemini-priority":    {"/v1beta/models/m:generateContent", `{"serviceTier":"priority","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`},
		"gemini-standard":    {"/v1beta/models/m:generateContent", `{"serviceTier":"standard","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`},
	}
	// want is, per client and wire, the fields of the upstream body that say a tier or a
	// verbosity: "service_tier", "verbosity", "text.verbosity", "speed" or "serviceTier" with the
	// value; none for an upstream that is sent neither.
	want := map[string]map[string]map[string]string{
		"chat-priority":      {"chat": {"service_tier": "priority", "verbosity": "low"}, "responses": {"service_tier": "priority", "text.verbosity": "low"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
		"chat-fast":          {"chat": {"service_tier": "fast"}, "responses": {"service_tier": "fast"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
		"chat-flex":          {"chat": {"service_tier": "flex"}, "responses": {"service_tier": "flex"}, "messages": {}, "gemini": {"serviceTier": "flex"}},
		"chat-auto":          {"chat": {"service_tier": "auto"}, "responses": {"service_tier": "auto"}, "messages": {"service_tier": "auto"}, "gemini": {}},
		"responses-priority": {"chat": {"service_tier": "priority", "verbosity": "high"}, "responses": {"service_tier": "priority", "text.verbosity": "high"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
		"messages-fast":      {"chat": {"service_tier": "priority"}, "responses": {"service_tier": "priority"}, "messages": {"speed": "fast"}, "gemini": {"serviceTier": "priority"}},
		"messages-auto":      {"chat": {"service_tier": "auto"}, "responses": {"service_tier": "auto"}, "messages": {"service_tier": "auto"}, "gemini": {}},
		"gemini-priority":    {"chat": {"service_tier": "priority"}, "responses": {"service_tier": "priority"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
		"gemini-standard":    {"chat": {"service_tier": "default"}, "responses": {"service_tier": "default"}, "messages": {"service_tier": "standard_only"}, "gemini": {"serviceTier": "standard"}},
	}
	for name, c := range clients {
		for _, wire := range []string{"chat", "responses", "messages", "gemini"} {
			if want[name][wire] == nil {
				t.Fatalf("no expectation for %s on %s", name, wire)
			}
			t.Run(name+" to "+wire, func(t *testing.T) {
				// A request to a Messages wire needs an Anthropic-Version header only from its own
				// clients, so none is sent for the others.
				var mu sync.Mutex
				var bodies [][]byte
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "application/json")
					if r.Method != http.MethodPost {
						// The account check and the model list.
						io.WriteString(w, `{"data":[]}`)
						return
					}
					mu.Lock()
					bodies = append(bodies, b)
					mu.Unlock()
					io.WriteString(w, wireReplies[wire])
				}))
				t.Cleanup(srv.Close)
				base := runCore(t, map[string]any{
					"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
					"extension": map[string]any{
						"manifest": filepath.Join(binDir, "pluxy-extension.json"),
						"accounts": []any{map[string]any{
							"id":         "upstream-1",
							"settings":   map[string]any{"base_url": srv.URL + "/v1", "wire_api": wire},
							"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
						}},
					},
				})
				req, _ := http.NewRequest(http.MethodPost, base+c.path, strings.NewReader(c.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+clientKey)
				req.Header.Set("X-Api-Key", clientKey)
				if strings.HasPrefix(c.path, "/v1/messages") {
					req.Header.Set("Anthropic-Version", "2023-06-01")
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "Done.") {
					t.Fatalf("status %d: %s", resp.StatusCode, raw)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(bodies) != 1 {
					t.Fatalf("upstream got %d requests, want 1", len(bodies))
				}
				var sent map[string]json.RawMessage
				if err := json.Unmarshal(bodies[0], &sent); err != nil {
					t.Fatal(err)
				}
				got := map[string]string{}
				for _, f := range []string{"service_tier", "verbosity", "speed", "serviceTier"} {
					if v, ok := sent[f]; ok {
						var s string
						_ = json.Unmarshal(v, &s)
						got[f] = s
					}
				}
				var text struct {
					Verbosity string `json:"verbosity"`
				}
				_ = json.Unmarshal(sent["text"], &text)
				if text.Verbosity != "" {
					got["text.verbosity"] = text.Verbosity
				}
				if !maps.Equal(got, want[name][wire]) {
					t.Errorf("upstream body says %v, want %v: %s", got, want[name][wire], bodies[0])
				}
			})
		}
	}
}

// Under the reject drop policy a tier the upstream's wire cannot say refuses the request before it
// reaches the upstream, naming the field the client wrote it in; one it can say goes.
func TestServiceTierRejectPolicy(t *testing.T) {
	for _, c := range []struct {
		name, path, body, wire string
		dropped                string // the field the refusal names; "" when the request goes
	}{
		{"chat priority to messages", "/v1/chat/completions", `{"model":"m","service_tier":"priority","messages":[{"role":"user","content":"Hi"}]}`, "messages", "service_tier"},
		{"chat flex to messages", "/v1/chat/completions", `{"model":"m","service_tier":"flex","messages":[{"role":"user","content":"Hi"}]}`, "messages", "service_tier"},
		{"chat verbosity to gemini", "/v1/chat/completions", `{"model":"m","verbosity":"low","messages":[{"role":"user","content":"Hi"}]}`, "gemini", "verbosity"},
		{"chat auto to gemini", "/v1/chat/completions", `{"model":"m","service_tier":"auto","messages":[{"role":"user","content":"Hi"}]}`, "gemini", "service_tier"},
		{"messages fast to chat", "/v1/messages", `{"model":"m","max_tokens":9,"speed":"fast","messages":[{"role":"user","content":"Hi"}]}`, "chat", ""},
		{"gemini flex to responses", "/v1beta/models/m:generateContent", `{"serviceTier":"flex","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`, "responses", ""},
		{"chat auto to messages", "/v1/chat/completions", `{"model":"m","service_tier":"auto","messages":[{"role":"user","content":"Hi"}]}`, "messages", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodPost {
					// The account check and the model list.
					io.WriteString(w, `{"data":[]}`)
					return
				}
				mu.Lock()
				calls++
				mu.Unlock()
				io.WriteString(w, wireReplies[c.wire])
			}))
			t.Cleanup(srv.Close)
			base := runCore(t, map[string]any{
				"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
				"drop_policy": "reject",
				"extension": map[string]any{
					"manifest": filepath.Join(binDir, "pluxy-extension.json"),
					"accounts": []any{map[string]any{
						"id":         "upstream-1",
						"settings":   map[string]any{"base_url": srv.URL + "/v1", "wire_api": c.wire},
						"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
					}},
				},
			})
			req, _ := http.NewRequest(http.MethodPost, base+c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+clientKey)
			req.Header.Set("X-Api-Key", clientKey)
			if strings.HasPrefix(c.path, "/v1/messages") {
				req.Header.Set("Anthropic-Version", "2023-06-01")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			if c.dropped == "" {
				if resp.StatusCode != http.StatusOK || calls != 1 {
					t.Errorf("status %d with %d upstream calls: %s; want the request sent", resp.StatusCode, calls, raw)
				}
				return
			}
			if resp.StatusCode == http.StatusOK || calls != 0 || !strings.Contains(string(raw), c.dropped) {
				t.Errorf("status %d with %d upstream calls: %s; want it refused naming %s", resp.StatusCode, calls, raw, c.dropped)
			}
		})
	}
}
