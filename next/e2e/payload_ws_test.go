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
// answered.
type payloadWSUpstream struct {
	*httptest.Server
	replies wsUpstream // only its reply() is used

	mu       sync.Mutex
	messages [][]byte // every message received, in order
	sockets  int
	posts    int
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
			held := len(m.Input) > 0 && len(m.Input[0].Content) > 0 && m.Input[0].Content[0].Text == "hold"
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
