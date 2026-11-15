package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// payloadWSUpstream is a fake Codex upstream that records every message each WebSocket receives,
// as the bytes that arrived. It answers a response.create with a short reply. When the turn's
// input text is "hold" it stops after the first event and waits for the next message on the socket
// (the client's interrupt), then finishes the reply; any other message is recorded and not
// answered. When it is "drop" the upstream closes the socket after the reply.
type payloadWSUpstream struct {
	*httptest.Server
	replies wsUpstream // only its reply() is used

	mu       sync.Mutex
	messages [][]byte // every message received, in order
	sockets  int
	posts    int
	ended    int // sockets whose handler returned
}

func newPayloadWSUpstream(t *testing.T) *payloadWSUpstream {
	u := &payloadWSUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodGet {
			u.mu.Lock()
			u.posts++
			u.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		u.mu.Lock()
		u.sockets++
		u.mu.Unlock()
		defer func() {
			u.mu.Lock()
			u.ended++
			u.mu.Unlock()
		}()
		read := func() ([]byte, bool) {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return nil, false
			}
			u.mu.Lock()
			u.messages = append(u.messages, data)
			u.mu.Unlock()
			return data, true
		}
		for {
			data, ok := read()
			if !ok {
				return
			}
			var m struct {
				Type  string `json:"type"`
				Input []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
			}
			_ = json.Unmarshal(data, &m)
			if m.Type != "response.create" {
				continue
			}
			text := ""
			if len(m.Input) > 0 && len(m.Input[0].Content) > 0 {
				text = m.Input[0].Content[0].Text
			}
			held := text == "hold"
			for i, ev := range u.replies.reply() {
				if c.Write(context.Background(), websocket.MessageText, ev) != nil {
					return
				}
				if i == 0 && held {
					if _, ok := read(); !ok {
						return
					}
				}
			}
			if text == "drop" {
				_ = c.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// received returns the messages received so far, each decoded.
func (u *payloadWSUpstream) received(t *testing.T) (raw [][]byte, decoded []map[string]any) {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, b := range u.messages {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("upstream got a message that is not JSON: %q", b)
		}
		raw = append(raw, append([]byte(nil), b...))
		decoded = append(decoded, m)
	}
	return raw, decoded
}

// wsTurnWithTools is a Responses WebSocket turn carrying the tools of the old engine's oracle case
// "example filter codex flat tools, two patches: first only" (sdk/payload/testdata/cases.json),
// each with a description so the tools can be told apart.
func wsTurnWithTools(text string) map[string]any {
	m := wsCreate(text, "")
	tool := func(name, desc string) map[string]any {
		return map[string]any{"type": "function", "name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}}}
	}
	m["tools"] = []any{tool("apply_patch", "first"), tool("read", "read"), tool("apply_patch", "second")}
	return m
}

// descriptions returns the descriptions of a message's function tools (those with a name; the
// extension adds an image_generation tool of its own), in order.
func descriptions(m map[string]any) []string {
	var out []string
	tools, _ := m["tools"].([]any)
	for _, tool := range tools {
		if t, ok := tool.(map[string]any); ok && t["name"] != nil {
			d, _ := t["description"].(string)
			out = append(out, d)
		}
	}
	return out
}

// The check of the slice that gives payload rules to turns on an upstream WebSocket: the
// response.create that goes upstream has the rules applied to it once, whether it is a socket's
// first turn or a later one, and a message that is no turn (the client's interrupt) goes through
// as it is. The rules are the example config's Codex ones (core/config.example.yaml):
// reasoning.effort "high" for gpt-* and the filter of the first apply_patch tool, whose expected
// result is the oracle case's: of two apply_patch tools only the second is left, so applying the
// filter twice would leave none.
func TestPayloadWebSocketTurns(t *testing.T) {
	u := newPayloadWSUpstream(t)
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"payload": map[string]any{
			"override": []any{map[string]any{
				"models": []any{map[string]any{"name": "gpt-*", "protocol": "codex"}},
				"params": map[string]any{"reasoning.effort": "high"},
			}},
			"filter": []any{map[string]any{
				"models": []any{map[string]any{"name": "gpt-*", "protocol": "codex"}},
				"params": []string{`tools.#(name=="apply_patch")`},
			}},
		},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "acct-ws",
				"models":     []string{"gpt-test"},
				"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": true},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-ws"},
			}},
		},
	})

	c := dialResponses(t, base)
	first := wsTurn(t, c, wsTurnWithTools("one"))
	second := wsTurn(t, c, wsTurnWithTools("two"))
	for i, m := range []map[string]any{first, second} {
		if m["type"] != "response.completed" {
			t.Fatalf("turn %d ended with %v", i+1, m)
		}
	}

	// A third turn is interrupted while it runs: the interrupt goes upstream as the client sent it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hold, _ := json.Marshal(wsTurnWithTools("hold"))
	if err := c.Write(ctx, websocket.MessageText, hold); err != nil {
		t.Fatal(err)
	}
	var id string
	for id == "" {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for the third turn to start: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		if m["type"] == "response.created" {
			id = responseID(m)
		}
	}
	interrupt := []byte(`{"type":"response.interrupt","response_id":"` + id + `"}`)
	if err := c.Write(ctx, websocket.MessageText, interrupt); err != nil {
		t.Fatal(err)
	}
	for done := false; !done; {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("reading the rest of the third turn: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		done = m["type"] == "response.completed"
	}

	raw, got := u.received(t)
	u.mu.Lock()
	sockets, posts := u.sockets, u.posts
	u.mu.Unlock()
	if sockets != 1 || posts != 0 || len(got) != 4 {
		t.Fatalf("upstream saw %d sockets, %d HTTP requests and %d messages, want 1, 0 and 4 (three turns and the interrupt)", sockets, posts, len(got))
	}
	for i, n := range []int{0, 1, 2} {
		m := got[n]
		if m["type"] != "response.create" {
			t.Fatalf("message %d is %s, want a response.create", n+1, raw[n])
		}
		if v, _ := at(m, "reasoning.effort"); v != "high" {
			t.Errorf("turn %d: reasoning.effort = %v, want high; message %s", i+1, v, raw[n])
		}
		if d := descriptions(m); !reflect.DeepEqual(d, []string{"read", "second"}) {
			t.Errorf("turn %d: tool descriptions %q, want [read second] (only the first apply_patch removed, once); message %s", i+1, d, raw[n])
		}
	}
	if !bytes.Equal(raw[3], interrupt) {
		t.Errorf("the interrupt went upstream as %s, want it as the client sent it, %s", raw[3], interrupt)
	}
}

// The response a turn continues is read from the message as it goes out, after the rules: a rule
// that removes previous_response_id makes the turn continue nothing. The upstream closes the
// socket the first turn ran on, so a turn that still named a previous response would be refused
// with previous_response_not_found; with the rule it names none, connects again and completes.
// An extension that read the response before applying the rules would refuse it.
func TestPayloadWebSocketRuleRemovesPreviousResponse(t *testing.T) {
	u := newPayloadWSUpstream(t)
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"payload": map[string]any{
			"filter": []any{map[string]any{
				"models": []any{map[string]any{"name": "gpt-*", "protocol": "codex"}},
				"params": []string{"previous_response_id"},
			}},
		},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "acct-ws",
				"models":     []string{"gpt-test"},
				"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": true},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-ws"},
			}},
		},
	})

	c := dialResponses(t, base)
	first := wsTurn(t, c, wsCreate("drop", ""))
	if first["type"] != "response.completed" {
		t.Fatalf("first turn ended with %v", first)
	}
	// Wait until the upstream has closed the socket, then a moment for the extension to see it end.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		u.mu.Lock()
		ended := u.ended
		u.mu.Unlock()
		if ended == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the upstream's socket did not end")
		}
	}
	time.Sleep(200 * time.Millisecond)

	second := wsTurn(t, c, wsCreate("two", responseID(first)))
	if second["type"] != "response.completed" {
		t.Fatalf("a turn naming a previous response ended with %v, want it completed: the rule removes the name, so it continues nothing", second)
	}
	raw, got := u.received(t)
	u.mu.Lock()
	sockets := u.sockets
	u.mu.Unlock()
	if sockets != 2 || len(got) != 2 {
		t.Fatalf("upstream saw %d sockets and %d messages, want 2 and 2", sockets, len(got))
	}
	if _, ok := got[1]["previous_response_id"]; ok {
		t.Errorf("the second message still names a previous response: %s", raw[1])
	}
}

// A turn core sends upstream as an HTTP call (the account takes no WebSocket) gets the rules once,
// from sdk.Upstream, as a session turn gets them once from sdk.ApplyPayload. The rules are those
// of TestPayloadWebSocketTurns: of the two apply_patch tools, applying the filter twice would
// leave neither, and not applying it would leave both.
func TestPayloadWebSocketTurnsOverHTTP(t *testing.T) {
	u := newWSUpstream(t)
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"payload": map[string]any{
			"override": []any{map[string]any{
				"models": []any{map[string]any{"name": "gpt-*", "protocol": "codex"}},
				"params": map[string]any{"reasoning.effort": "high"},
			}},
			"filter": []any{map[string]any{
				"models": []any{map[string]any{"name": "gpt-*", "protocol": "codex"}},
				"params": []string{`tools.#(name=="apply_patch")`},
			}},
		},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "acct-http",
				"models":     []string{"gpt-test"},
				"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": false},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-http"},
			}},
		},
	})

	c := dialResponses(t, base)
	first := wsTurn(t, c, wsTurnWithTools("one"))
	second := wsTurn(t, c, wsTurnWithTools("two"))
	for i, m := range []map[string]any{first, second} {
		if m["type"] != "response.completed" {
			t.Fatalf("turn %d ended with %v", i+1, m)
		}
	}
	u.mu.Lock()
	posts := append([]map[string]any(nil), u.posts...)
	u.mu.Unlock()
	if conns := u.connections(); len(conns) != 0 || len(posts) != 2 {
		t.Fatalf("upstream saw %d sockets and %d HTTP requests, want 0 and 2", len(conns), len(posts))
	}
	for i, m := range posts {
		if v, _ := at(m, "reasoning.effort"); v != "high" {
			t.Errorf("turn %d: reasoning.effort = %v, want high", i+1, v)
		}
		if d := descriptions(m); !reflect.DeepEqual(d, []string{"read", "second"}) {
			t.Errorf("turn %d: tool descriptions %q, want [read second] (only the first apply_patch removed, once)", i+1, d)
		}
	}
}

// An account that names its model differently from the client (an alias) is sent the upstream
// name, and the payload rules written for the upstream name apply to it, on a WebSocket turn as
// on POST /v1/responses: the turn takes the request the router made for the account, over an
// upstream session and as an HTTP call alike. The client asks for "my-gpt"; the account serves
// it as "gpt-test", and the rule matches only "gpt-test".
func TestPayloadAndUpstreamModelOfAliasedAccount(t *testing.T) {
	rules := map[string]any{
		"override": []any{map[string]any{
			"models": []any{map[string]any{"name": "gpt-test", "protocol": "codex"}},
			"params": map[string]any{"reasoning.effort": "high"},
		}},
	}
	start := func(t *testing.T, upstreamURL string, websockets bool) string {
		return runCore(t, map[string]any{
			"listen":      "127.0.0.1:0",
			"client_keys": []string{clientKey},
			"payload":     rules,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id":         "acct-alias",
					"models":     []any{map[string]any{"name": "gpt-test", "alias": "my-gpt"}},
					"settings":   map[string]any{"base_url": upstreamURL + "/v1", "websockets": websockets},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-alias"},
				}},
			},
		})
	}
	check := func(t *testing.T, m map[string]any) {
		t.Helper()
		if v, _ := at(m, "model"); v != "gpt-test" {
			t.Errorf("the upstream was asked for model %v, want gpt-test (the account's name for my-gpt)", v)
		}
		if v, _ := at(m, "reasoning.effort"); v != "high" {
			t.Errorf("reasoning.effort = %v, want high (the rule for gpt-test applies); message %v", v, m)
		}
	}
	turn := wsCreate("one", "")
	turn["model"] = "my-gpt"

	t.Run("HTTP", func(t *testing.T) {
		u := newWSUpstream(t)
		base := start(t, u.URL, false)
		resp := postPath(t, context.Background(), base, "/v1/responses", clientKey, `{"model":"my-gpt","input":"hi"}`)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/responses: status %d", resp.StatusCode)
		}
		u.mu.Lock()
		posts := append([]map[string]any(nil), u.posts...)
		u.mu.Unlock()
		if len(posts) != 1 {
			t.Fatalf("upstream saw %d HTTP requests, want 1", len(posts))
		}
		check(t, posts[0])
	})
	t.Run("WebSocket turn sent as HTTP", func(t *testing.T) {
		u := newWSUpstream(t)
		base := start(t, u.URL, false)
		c := dialResponses(t, base)
		first := wsTurn(t, c, turn)
		if first["type"] != "response.completed" {
			t.Fatalf("the first turn ended with %v", first)
		}
		// The second turn continues the first: it goes as an HTTP call with the whole conversation.
		second := wsCreate("two", responseID(first))
		second["model"] = "my-gpt"
		if m := wsTurn(t, c, second); m["type"] != "response.completed" {
			t.Fatalf("the second turn ended with %v", m)
		}
		u.mu.Lock()
		posts := append([]map[string]any(nil), u.posts...)
		u.mu.Unlock()
		if len(posts) != 2 {
			t.Fatalf("upstream saw %d HTTP requests, want 2", len(posts))
		}
		for _, p := range posts {
			check(t, p)
		}
	})
	t.Run("WebSocket turn on an upstream session", func(t *testing.T) {
		u := newPayloadWSUpstream(t)
		base := start(t, u.URL, true)
		c := dialResponses(t, base)
		first := wsTurn(t, c, turn)
		if first["type"] != "response.completed" {
			t.Fatalf("the first turn ended with %v", first)
		}
		// The second turn continues the first on the same session.
		second := wsCreate("two", responseID(first))
		second["model"] = "my-gpt"
		if m := wsTurn(t, c, second); m["type"] != "response.completed" {
			t.Fatalf("the second turn ended with %v", m)
		}
		_, got := u.received(t)
		if len(got) != 2 {
			t.Fatalf("upstream saw %d messages, want 2", len(got))
		}
		for _, m := range got {
			check(t, m)
		}
	})
}
