package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// payloadUpstream is a fake provider that records the body of each POST. It answers a count
// request with a token count, a streaming chat request with a short stream, the first POST with
// a 429 when throttleFirst is set, and anything else with a 400.
type payloadUpstream struct {
	*httptest.Server
	throttleFirst bool
	mu            sync.Mutex
	seen          []payloadPost
}

type payloadPost struct {
	path string
	body map[string]any
	raw  string
}

func newPayloadUpstream(t *testing.T, throttleFirst bool) *payloadUpstream {
	u := &payloadUpstream{throttleFirst: throttleFirst}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.seen = append(u.seen, payloadPost{r.URL.Path, body, string(raw)})
		first := len(u.seen) == 1
		u.mu.Unlock()
		switch {
		case u.throttleFirst && first:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"fake upstream: slow down","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
		case strings.HasSuffix(r.URL.Path, ":countTokens"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"totalTokens":3}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions") && body["stream"] == true:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, chunk := range []string{
				`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m-payload","choices":[{"index":0,"delta":{"role":"assistant","content":"streamed-ok"},"finish_reason":null}]}`,
				`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m-payload","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			} {
				_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"fake upstream refused it","type":"invalid_request_error","code":"fake"}}`)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// under returns the POSTs whose path starts with prefix.
func (u *payloadUpstream) under(prefix string) []payloadPost {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []payloadPost
	for _, p := range u.seen {
		if strings.HasPrefix(p.path, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// at returns the value at a dotted path of a decoded JSON body, and whether it is there.
func at(body map[string]any, path string) (any, bool) {
	var cur any = body
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// sameJSON reports whether v, decoded JSON, is the JSON text want.
func sameJSON(v any, want string) bool {
	var w any
	return json.Unmarshal([]byte(want), &w) == nil && reflect.DeepEqual(v, w)
}

// toolNames returns the names of the tools in a body, in order.
func toolNames(body map[string]any) []string {
	var names []string
	tools, _ := body["tools"].([]any)
	for _, tool := range tools {
		if m, ok := tool.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// clientPost sends a client request with body to path and returns the reply's status and body.
func clientPost(t *testing.T, base, path, body string, header map[string]string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// payloadChannels are the payload rules' protocol for each provider extension, as next/README.md
// lists them, and the member of its upstream body that holds the request ("" for the body).
// Devin's upstream body is protobuf, which payload rules do not apply to.
var payloadChannels = map[string]struct{ channel, root string }{
	"pluxy/openai-compatible": {"openai", ""},
	"pluxy/claude":            {"claude", ""},
	"pluxy/codex":             {"codex", ""},
	"pluxy/gemini":            {"gemini", ""},
	"pluxy/kimi":              {"kimi", ""},
	"pluxy/xai":               {"xai", ""},
	"pluxy/meta":              {"meta", ""},
	"pluxy/antigravity":       {"antigravity", "request"},
}

// The slice's check: every provider extension with a JSON upstream body applies the rules core
// matched to the call, inside the member that holds the request, and only the rules of its own
// channel.
func TestPayloadEveryExtension(t *testing.T) {
	for _, ext := range proxyExtensions {
		ch, ok := payloadChannels[ext.id]
		if !ok {
			continue
		}
		t.Run(strings.TrimPrefix(ext.id, "pluxy/"), func(t *testing.T) {
			t.Parallel()
			up := newPayloadUpstream(t, false)
			base := runCore(t, map[string]any{
				"listen":      "127.0.0.1:0",
				"client_keys": []string{clientKey},
				"payload": map[string]any{"override": []any{
					map[string]any{"models": []any{map[string]any{"name": "m-pay*", "protocol": ch.channel}}, "params": map[string]any{"pluxy_e2e": "set"}},
					map[string]any{"models": []any{map[string]any{"name": "m-payload", "protocol": "another-channel"}}, "params": map[string]any{"pluxy_wrong": "set"}},
				}},
				"extension": map[string]any{
					"manifest": filepath.Join(binDir, ext.manifest),
					"accounts": []any{map[string]any{"id": "a", "models": []string{"m-payload"}, "settings": map[string]any{"base_url": up.URL + "/a"},
						"credential": ext.credential}},
				},
			})
			pinned(t, base, "a", "m-payload")
			posts := up.under("/a")
			if len(posts) == 0 {
				t.Fatalf("no POST reached the upstream")
			}
			prefix := ""
			if ch.root != "" {
				prefix = ch.root + "."
			}
			for _, p := range posts {
				if v, _ := at(p.body, prefix+"pluxy_e2e"); v != "set" {
					t.Errorf("%s: %spluxy_e2e = %v, want \"set\"; body %s", p.path, prefix, v, p.raw)
				}
				if strings.Contains(p.raw, "pluxy_wrong") {
					t.Errorf("%s: the other channel's rule applied: %s", p.path, p.raw)
				}
			}
		})
	}
}

// The payload section of today's core/config.example.yaml, uncommented, as a config.yaml has it.
const examplePayload = `
payload:
  default: # Default rules only set parameters when they are missing in the payload.
    - models:
        - name: "gemini-2.5-pro" # Supports wildcards (e.g., "gemini-*")
          protocol: "gemini" # restricts the rule to a specific protocol, options: openai, gemini, claude, codex, antigravity
          from-protocol: "responses" # restricts the rule to the source protocol, options: openai, responses, gemini, claude
          headers: # all configured request headers must match; values support "*" wildcards
            X-Client-Tier: "tenant-*-region-*"
          match: # all payload JSON paths must equal the configured values
            - "metadata.client": "codex"
          not-match: # payload JSON paths must not equal the configured values
            - "metadata.mode": "dev"
          exist: # all payload JSON paths must exist and not be null
            - "tools.#(type==\"web_search\").type"
          not-exist: # all payload JSON paths must be missing or null
            - "metadata.disable_payload"
      params: # JSON path (gjson/sjson syntax) -> value
        "generationConfig.thinkingConfig.thinkingBudget": 32768
  default-raw: # Default raw rules set parameters using raw JSON when missing (must be valid JSON).
    - models:
        - name: "gemini-2.5-pro" # Supports wildcards (e.g., "gemini-*")
          protocol: "gemini" # restricts the rule to a specific protocol, options: openai, gemini, claude, codex, antigravity
      params: # JSON path (gjson/sjson syntax) -> raw JSON value (strings are used as-is, must be valid JSON)
        "generationConfig.responseJsonSchema": "{\"type\":\"object\",\"properties\":{\"answer\":{\"type\":\"string\"}}}"
  override: # Override rules always set parameters, overwriting any existing values.
    - models:
        - name: "gpt-5.4-fast"
          protocol: "codex"
        - name: "gpt-5.5-fast"
          protocol: "codex"
      params:
        service_tier: priority
    - models:
        - name: "gpt-*" # Supports wildcards (e.g., "gpt-*")
          protocol: "codex" # restricts the rule to a specific protocol, options: openai, gemini, claude, codex, antigravity
      params: # JSON path (gjson/sjson syntax) -> value
        "reasoning.effort": "high"
    - models:
        - name: "claude-fable-5-1"
          protocol: "claude"
      params:
        max_tokens: 128000 # override the final Claude Messages output budget after protocol translation
  override-raw: # Override raw rules always set parameters using raw JSON (must be valid JSON).
    - models:
        - name: "gpt-*" # Supports wildcards (e.g., "gpt-*")
          protocol: "codex" # restricts the rule to a specific protocol, options: openai, gemini, claude, codex, antigravity
      params: # JSON path (gjson/sjson syntax) -> raw JSON value (strings are used as-is, must be valid JSON)
        "response_format": "{\"type\":\"json_schema\",\"json_schema\":{\"name\":\"answer\",\"schema\":{\"type\":\"object\"}}}"
  filter: # Filter rules remove specified parameters from the payload.
    - models:
        - name: "gemini-2.5-pro" # Supports wildcards (e.g., "gemini-*")
          protocol: "gemini" # restricts the rule to a specific protocol, options: openai, gemini, claude, codex, antigravity
      params: # JSON paths (gjson/sjson syntax) to remove from the payload
        - "generationConfig.thinkingConfig.thinkingBudget"
        - "generationConfig.responseJsonSchema"
    - models:
        - name: "gpt-*" # Supports wildcards (e.g., "gpt-*")
          protocol: "codex"
      params:
        # Flat tools array (standard OpenAI format or Codex CLI < 0.154):
        - 'tools.#(name=="apply_patch")'
        # Nested additional_tools structure in input[] (Codex CLI >= 0.154 "Responses Lite"):
        - 'input.#(type=="additional_tools")#.tools.#(name=="functions")#.tools.#(name=="apply_patch")#'
`

// Today's example payload blocks work in a config.yaml: Codex gets service_tier priority on
// gpt-5.4-fast, reasoning effort high, the raw response_format and no apply_patch tool; Claude
// gets max_tokens 128000 over the client's; Gemini gets the thinking budget and the schema
// removed (its filter runs after its default-raw). No notice calls payload unsupported.
func TestPayloadExampleConfigYAML(t *testing.T) {
	up := newPayloadUpstream(t, false)
	cfg := `
host: 127.0.0.1
port: ` + strconv.Itoa(freePort(t)) + `
access:
  api-keys:
    - "` + clientKey + `"
api-keys:
  codex:
    - name: cx
      base-url: ` + up.URL + `/codex
      models:
        - name: gpt-5.4-fast
      keys:
        - api-key: ` + upstreamKey + `
  claude:
    - name: cl
      base-url: ` + up.URL + `/claude
      models:
        - name: claude-fable-5-1
      keys:
        - api-key: ` + upstreamKey + `
  gemini:
    - name: gm
      base-url: ` + up.URL + `/gemini
      models:
        - name: gemini-2.5-pro
      keys:
        - api-key: ` + upstreamKey + `
requests:` + strings.ReplaceAll(examplePayload, "\n", "\n  ")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	bundled := bundledRegistry(t, map[string]string{
		"pluxy/codex":  filepath.Join("codex", "codex"),
		"pluxy/claude": filepath.Join("claude", "claude"),
		"pluxy/gemini": filepath.Join("gemini", "gemini"),
	})
	base, logs := runCoreFile(t, path, "-bundled", bundled)
	logs.waitFor(t, "config revision 1 active")
	logs.mu.Lock()
	for _, line := range logs.lines {
		if strings.Contains(line, "payload") {
			t.Errorf("core's log names payload: %s", line)
		}
	}
	logs.mu.Unlock()

	clientPost(t, base, "/v1/responses", `{"model":"gpt-5.4-fast","input":"hi","reasoning":{"effort":"low"},"tools":[`+
		`{"type":"function","name":"apply_patch","description":"patch","parameters":{"type":"object","properties":{}}},`+
		`{"type":"function","name":"shell","description":"run","parameters":{"type":"object","properties":{}}}]}`, nil)
	clientPost(t, base, "/v1/chat/completions", `{"model":"claude-fable-5-1","max_tokens":1000,"messages":[{"role":"user","content":"hi"}]}`, nil)
	clientPost(t, base, "/v1beta/models/gemini-2.5-pro:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],`+
		`"generationConfig":{"temperature":0.5,"thinkingConfig":{"thinkingBudget":1024}}}`, nil)

	codex, claude, gemini := up.under("/codex/"), up.under("/claude/"), up.under("/gemini/")
	if len(codex) != 1 || len(claude) != 1 || len(gemini) != 1 {
		t.Fatalf("POSTs at the upstream: codex %d, claude %d, gemini %d, want 1 each", len(codex), len(claude), len(gemini))
	}
	c := codex[0].body
	// Expected values: the example's rules, as the old engine applies them (sdk/payload testdata
	// cases "example override codex service_tier on gpt-5.4-fast", "example override-raw
	// response_format", "example filter codex flat tools, patch is first").
	if v, _ := at(c, "service_tier"); v != "priority" {
		t.Errorf("codex service_tier = %v, want priority; body %s", v, codex[0].raw)
	}
	if v, _ := at(c, "reasoning.effort"); v != "high" {
		t.Errorf("codex reasoning.effort = %v, want high; body %s", v, codex[0].raw)
	}
	if v, _ := at(c, "response_format"); !sameJSON(v, `{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}`) {
		t.Errorf("codex response_format = %v; body %s", v, codex[0].raw)
	}
	if names := toolNames(c); !reflect.DeepEqual(names, []string{"shell"}) {
		t.Errorf("codex tool names %q, want only shell; body %s", names, codex[0].raw)
	}
	// "example override claude max_tokens".
	if v, _ := at(claude[0].body, "max_tokens"); v != float64(128000) {
		t.Errorf("claude max_tokens = %v, want 128000; body %s", v, claude[0].raw)
	}
	// "example filter gemini, both present": the default-raw writes the schema, the filter
	// removes it and the client's thinking budget; the rest of generationConfig stays.
	g := gemini[0].body
	if _, ok := at(g, "generationConfig.thinkingConfig.thinkingBudget"); ok {
		t.Errorf("gemini kept thinkingBudget; body %s", gemini[0].raw)
	}
	if _, ok := at(g, "generationConfig.responseJsonSchema"); ok {
		t.Errorf("gemini has responseJsonSchema; body %s", gemini[0].raw)
	}
	if v, _ := at(g, "generationConfig.temperature"); v != 0.5 {
		t.Errorf("gemini generationConfig.temperature = %v, want 0.5; body %s", v, gemini[0].raw)
	}
}

// A default fills in a field the upstream body lacks and keeps the client's own, and a rule's
// header condition matches the client's request headers ("example default gemini
// thinkingBudget, every condition holds", "... field already present", "... header missing").
func TestPayloadDefaultAndClientHeaders(t *testing.T) {
	up := newPayloadUpstream(t, false)
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"payload": map[string]any{"default": []any{map[string]any{
			"models": []any{map[string]any{"name": "gemini-2.5-pro", "protocol": "gemini", "headers": map[string]string{"X-Client-Tier": "tenant-*-region-*"}}},
			"params": map[string]any{"generationConfig.thinkingConfig.thinkingBudget": 32768},
		}}},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "gemini", "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "g", "models": []string{"gemini-2.5-pro"}, "settings": map[string]any{"base_url": up.URL + "/g"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
		},
	})
	tier := map[string]string{"X-Client-Tier": "tenant-7-region-eu"}
	chat := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`
	for _, c := range []struct {
		name, path, body string
		header           map[string]string
		want             any // nil: no budget
	}{
		{"missing", "/v1/chat/completions", chat, tier, float64(32768)},
		{"the client's", "/v1beta/models/gemini-2.5-pro:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}`, tier, float64(1024)},
		{"no header", "/v1/chat/completions", chat, nil, nil},
		{"other tier", "/v1/chat/completions", chat, map[string]string{"X-Client-Tier": "free"}, nil},
	} {
		before := len(up.under("/g/"))
		clientPost(t, base, c.path, c.body, c.header)
		posts := up.under("/g/")
		if len(posts) != before+1 {
			t.Fatalf("%s: %d new POSTs, want 1", c.name, len(posts)-before)
		}
		p := posts[len(posts)-1]
		got, ok := at(p.body, "generationConfig.thinkingConfig.thinkingBudget")
		if c.want == nil && ok || c.want != nil && got != c.want {
			t.Errorf("%s: thinkingBudget %v (present %v), want %v; body %s", c.name, got, ok, c.want, p.raw)
		}
	}
}

// A request that a 429 moves to another account has the rules applied to its body on each
// account once: of two apply_patch tools the filter removes only the first ("example filter
// codex flat tools, two patches: first only"), so applying it twice would remove both.
func TestPayloadRetryAppliedOnce(t *testing.T) {
	up := newPayloadUpstream(t, true)
	account := func(id string) map[string]any {
		return map[string]any{"id": id, "models": []string{"gpt-5.4"}, "settings": map[string]any{"base_url": up.URL + "/" + id},
			"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}
	}
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"payload": map[string]any{"filter": []any{map[string]any{
			"models": []any{map[string]any{"name": "gpt-*", "protocol": "codex"}},
			"params": []string{`tools.#(name=="apply_patch")`},
		}}},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{account("one"), account("two")},
		},
	})
	clientPost(t, base, "/v1/responses", `{"model":"gpt-5.4","input":"hi","tools":[`+
		`{"type":"function","name":"apply_patch","description":"first","parameters":{"type":"object","properties":{}}},`+
		`{"type":"function","name":"apply_patch","description":"second","parameters":{"type":"object","properties":{}}},`+
		`{"type":"function","name":"shell","description":"run","parameters":{"type":"object","properties":{}}}]}`, nil)
	up.mu.Lock()
	seen := append([]payloadPost(nil), up.seen...)
	up.mu.Unlock()
	if len(seen) != 2 || strings.SplitN(seen[0].path, "/", 3)[1] == strings.SplitN(seen[1].path, "/", 3)[1] {
		var paths []string
		for _, p := range seen {
			paths = append(paths, p.path)
		}
		t.Fatalf("POSTs %q, want one on each account (the first answered 429)", paths)
	}
	for i, p := range seen {
		var descs []string
		tools, _ := p.body["tools"].([]any)
		for _, tool := range tools {
			if m, _ := tool.(map[string]any); m["name"] == "apply_patch" {
				descs = append(descs, m["description"].(string))
			}
		}
		if !reflect.DeepEqual(descs, []string{"second"}) {
			t.Errorf("attempt %d (%s): apply_patch tools %q, want only the second; body %s", i+1, p.path, descs, p.raw)
		}
	}
}

// Rules apply to a streaming request's body, which streams back to the client, to a token count
// and to an endpoint call (Responses compaction).
func TestPayloadStreamingCountAndEndpoint(t *testing.T) {
	rules := map[string]any{"override": []any{map[string]any{"models": []any{map[string]any{"name": "m-payload"}}, "params": map[string]any{"pluxy_e2e": "set"}}}}
	core := func(up *payloadUpstream, manifest string) string {
		return runCore(t, map[string]any{
			"listen":      "127.0.0.1:0",
			"client_keys": []string{clientKey},
			"payload":     rules,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, manifest),
				"accounts": []any{map[string]any{"id": "a", "models": []string{"m-payload"}, "settings": map[string]any{"base_url": up.URL + "/a"},
					"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
			},
		})
	}
	check := func(t *testing.T, up *payloadUpstream, suffix string) map[string]any {
		t.Helper()
		var hit []payloadPost
		for _, p := range up.under("/a") {
			if strings.HasSuffix(p.path, suffix) {
				hit = append(hit, p)
			}
		}
		if len(hit) != 1 {
			t.Fatalf("%d POSTs ending in %s, want 1", len(hit), suffix)
		}
		if v, _ := at(hit[0].body, "pluxy_e2e"); v != "set" {
			t.Errorf("%s: pluxy_e2e = %v, want set; body %s", hit[0].path, v, hit[0].raw)
		}
		return hit[0].body
	}
	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		up := newPayloadUpstream(t, false)
		status, reply := clientPost(t, core(up, "pluxy-extension.json"), "/v1/chat/completions",
			`{"model":"m-payload","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
		if status != http.StatusOK || !strings.Contains(reply, "streamed-ok") {
			t.Errorf("client got %d: %s", status, reply)
		}
		if body := check(t, up, "/chat/completions"); body["stream"] != true {
			t.Errorf("the upstream request does not stream: %v", body)
		}
	})
	t.Run("count", func(t *testing.T) {
		t.Parallel()
		up := newPayloadUpstream(t, false)
		status, reply := clientPost(t, core(up, filepath.Join("gemini", "pluxy-extension.json")), "/v1beta/models/m-payload:countTokens",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil)
		if status != http.StatusOK || !strings.Contains(reply, "3") {
			t.Errorf("client got %d: %s", status, reply)
		}
		check(t, up, ":countTokens")
	})
	t.Run("endpoint", func(t *testing.T) {
		t.Parallel()
		up := newPayloadUpstream(t, false)
		clientPost(t, core(up, filepath.Join("codex", "pluxy-extension.json")), "/v1/responses/compact", `{"model":"m-payload","input":"hi"}`, nil)
		check(t, up, "/responses/compact")
	})
}

// A rule path outside the supported syntax, or a raw value that is not JSON, is refused when the
// config loads and in a settings change, with the rule and path named; a valid change comes back
// from GET /settings as sent, applies to the next request, and null removes it.
func TestPayloadRefusedAndManaged(t *testing.T) {
	badPath := map[string]any{"filter": []any{map[string]any{"models": []any{map[string]any{"name": "m-payload"}}, "params": []string{"tools.*.name"}}}}
	badRaw := map[string]any{"default-raw": []any{map[string]any{"models": []any{map[string]any{"name": "m-payload"}}, "params": map[string]any{"a": "{not json"}}}}
	const pathMsg, rawMsg = `payload.filter[0].params: path "tools.*.name"`, `payload.default-raw[0].params["a"]: the raw value is not valid JSON`
	up := newPayloadUpstream(t, false)
	cfg := func(rules map[string]any) map[string]any {
		c := map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{map[string]any{"id": "a", "models": []string{"m-payload"}, "settings": map[string]any{"base_url": up.URL + "/a"},
					"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
			},
		}
		if rules != nil {
			c["payload"] = rules
		}
		return c
	}
	for _, c := range []struct {
		rules map[string]any
		msg   string
	}{{badPath, pathMsg}, {badRaw, rawMsg}} {
		b, _ := json.Marshal(cfg(c.rules))
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		raw, err := exec.CommandContext(ctx, filepath.Join(binDir, "pluxy"), "-config", path).CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if out := string(raw); !errors.As(err, &exit) || strings.Contains(out, "listening on") || !strings.Contains(out, c.msg) {
			t.Errorf("core did not refuse the config naming %q (%v):\n%s", c.msg, err, out)
		}
	}

	base, path, _ := runCoreArgs(t, cfg(nil))
	before, _ := os.ReadFile(path)
	for _, c := range []struct {
		rules map[string]any
		msg   string
	}{{badPath, pathMsg}, {badRaw, rawMsg}} {
		var reply struct{ Error struct{ Message string } }
		if code := manage(t, base, "PATCH", "/settings", map[string]any{"payload": c.rules}, &reply); code != http.StatusBadRequest || !strings.Contains(reply.Error.Message, c.msg) {
			t.Errorf("PATCH /settings: status %d, error %q, want 400 naming %q", code, reply.Error.Message, c.msg)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("a refused change wrote the config file:\n%s", after)
	}

	good := map[string]any{"override": []any{map[string]any{"models": []any{map[string]any{"name": "m-payload", "protocol": "openai"}},
		"params": map[string]any{"pluxy_e2e": "set", "n": 128000}}}}
	var settings struct{ Payload json.RawMessage }
	if code := manage(t, base, "PATCH", "/settings", map[string]any{"payload": good}, nil); code != http.StatusOK {
		t.Fatalf("PATCH /settings with valid rules: status %d", code)
	}
	if code := manage(t, base, "GET", "/settings", nil, &settings); code != http.StatusOK {
		t.Fatalf("GET /settings: status %d", code)
	}
	want, _ := json.Marshal(good)
	var got any
	_ = json.Unmarshal(settings.Payload, &got)
	if !sameJSON(got, string(want)) {
		t.Errorf("GET /settings payload %s, want %s", settings.Payload, want)
	}
	pinned(t, base, "a", "m-payload")
	posts := up.under("/a")
	if len(posts) != 1 {
		t.Fatalf("%d POSTs, want 1", len(posts))
	}
	if v, _ := at(posts[0].body, "pluxy_e2e"); v != "set" {
		t.Errorf("after the change: pluxy_e2e = %v, want set; body %s", v, posts[0].raw)
	}
	if v, _ := at(posts[0].body, "n"); v != float64(128000) {
		t.Errorf("after the change: n = %v, want 128000; body %s", v, posts[0].raw)
	}

	if code := manage(t, base, "PATCH", "/settings", map[string]any{"payload": nil}, nil); code != http.StatusOK {
		t.Fatalf("PATCH /settings payload null: status %d", code)
	}
	var shown, file map[string]json.RawMessage
	manage(t, base, "GET", "/settings", nil, &shown)
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	if _, ok := shown["payload"]; ok {
		t.Errorf("payload null: GET /settings still has payload %s", shown["payload"])
	}
	if _, ok := file["payload"]; ok {
		t.Errorf("payload null: the config file still has payload %s", file["payload"])
	}
}
