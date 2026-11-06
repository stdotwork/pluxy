package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsUpstream is a fake Codex upstream at /v1/responses: it answers a WebSocket upgrade by
// answering each response.create on the socket, and a POST with a streamed reply. Each reply
// says the text "reply <n>", n counting every reply it gave.
type wsUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	n     int
	conns []*wsUpstreamConn
	posts []map[string]any
	hold  chan struct{} // when set, the first WebSocket reply stops after response.created and the socket ends
}

type wsUpstreamConn struct {
	key      string
	messages []map[string]any
	closed   chan struct{} // closed when the core's side of the socket ended
}

func newWSUpstream(t *testing.T) *wsUpstream {
	u := &wsUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			u.serveWebSocket(w, r)
			return
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		u.mu.Lock()
		u.posts = append(u.posts, body)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range u.reply() {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// reply returns the events of the next reply.
func (u *wsUpstream) reply() [][]byte {
	u.mu.Lock()
	u.n++
	n := u.n
	u.mu.Unlock()
	id, text := fmt.Sprintf("resp_%d", n), fmt.Sprintf("reply %d", n)
	item := map[string]any{"id": fmt.Sprintf("msg_%d", n), "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	resp := func(status string, output []any) map[string]any {
		return map[string]any{"id": id, "object": "response", "status": status, "model": "gpt-test", "output": output}
	}
	var out [][]byte
	for _, ev := range []map[string]any{
		{"type": "response.created", "sequence_number": 0, "response": resp("in_progress", []any{})},
		{"type": "response.output_text.delta", "sequence_number": 1, "output_index": 0, "item_id": item["id"], "content_index": 0, "delta": text},
		{"type": "response.output_item.done", "sequence_number": 2, "output_index": 0, "item": item},
		{"type": "response.completed", "sequence_number": 3, "response": resp("completed", []any{item})},
	} {
		b, _ := json.Marshal(ev)
		out = append(out, b)
	}
	return out
}

func (u *wsUpstream) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	conn := &wsUpstreamConn{key: keyOf(r), closed: make(chan struct{})}
	defer close(conn.closed)
	u.mu.Lock()
	u.conns = append(u.conns, conn)
	hold := u.hold
	u.mu.Unlock()
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			return
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		u.mu.Lock()
		conn.messages = append(conn.messages, m)
		u.mu.Unlock()
		for i, ev := range u.reply() {
			if c.Write(context.Background(), websocket.MessageText, ev) != nil {
				return
			}
			if i == 0 && hold != nil {
				// Held: wait for the release or for the core to close the socket (a hijacked
				// request's context does not end with its connection).
				ended := make(chan struct{})
				go func() {
					_, _, _ = c.Read(context.Background())
					close(ended)
				}()
				select {
				case <-hold:
				case <-ended:
				}
				return
			}
		}
	}
}

// connections returns a copy of what each upstream WebSocket received so far.
func (u *wsUpstream) connections() []wsUpstreamConn {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []wsUpstreamConn
	for _, c := range u.conns {
		out = append(out, wsUpstreamConn{key: c.key, messages: append([]map[string]any(nil), c.messages...), closed: c.closed})
	}
	return out
}

// startWSCore runs core with a Codex account for each key; websockets turns the upstream
// WebSocket on for all of them.
func startWSCore(t *testing.T, u *wsUpstream, websockets bool, keys ...string) string {
	t.Helper()
	var accounts []any
	for _, k := range keys {
		accounts = append(accounts, map[string]any{
			"id":         "acct-" + k,
			"models":     []string{"gpt-test"},
			"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": websockets},
			"credential": map[string]any{"kind": "api_key", "api_key": k},
		})
	}
	return runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"routing":     map[string]any{"strategy": "round-robin"},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": accounts,
		},
	})
}

// dialResponses opens a Responses WebSocket on core.
func dialResponses(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := http.Header{"Authorization": {"Bearer " + clientKey}}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func wsCreate(text, previous string) map[string]any {
	m := map[string]any{"type": "response.create", "model": "gpt-test",
		"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}}
	if previous != "" {
		m["previous_response_id"] = previous
	}
	return m
}

// wsTurn sends msg and reads messages until the turn's terminal one, which it returns.
func wsTurn(t *testing.T, c *websocket.Conn, msg map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, _ := json.Marshal(msg)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("reading the turn: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("message %q: %v", data, err)
		}
		switch m["type"] {
		case "response.completed", "response.incomplete", "response.failed", "error":
			return m
		}
	}
}

func responseID(m map[string]any) string {
	r, _ := m["response"].(map[string]any)
	id, _ := r["id"].(string)
	return id
}

// A client socket stays on one account for all its turns, over one upstream WebSocket, though
// routing would rotate: a turn continues the previous response with previous_response_id on the
// same upstream socket. Another client socket is routed afresh and takes the other account.
func TestResponsesWebSocketStaysOnOneAccount(t *testing.T) {
	u := newWSUpstream(t)
	base := startWSCore(t, u, true, "sk-a", "sk-b")

	c := dialResponses(t, base)
	first := wsTurn(t, c, wsCreate("one", ""))
	if first["type"] != "response.completed" {
		t.Fatalf("first turn ended with %v", first)
	}
	second := wsTurn(t, c, wsCreate("two", responseID(first)))
	third := wsTurn(t, c, wsCreate("three", ""))
	for i, m := range []map[string]any{second, third} {
		if m["type"] != "response.completed" {
			t.Fatalf("turn %d ended with %v", i+2, m)
		}
	}
	conns := u.connections()
	if len(conns) != 1 || len(conns[0].messages) != 3 {
		t.Fatalf("upstream sockets: %+v; want one carrying the three turns", conns)
	}
	msgs := conns[0].messages
	if msgs[1]["previous_response_id"] != responseID(first) || msgs[1]["type"] != "response.create" {
		t.Errorf("second turn went upstream as %v; want a response.create continuing %s", msgs[1], responseID(first))
	}
	if _, ok := msgs[2]["previous_response_id"]; ok {
		t.Errorf("third turn went upstream as %v; want no previous_response_id", msgs[2])
	}

	other := dialResponses(t, base)
	if m := wsTurn(t, other, wsCreate("hello", "")); m["type"] != "response.completed" {
		t.Fatalf("other socket's turn ended with %v", m)
	}
	conns = u.connections()
	if len(conns) != 2 || conns[0].key == conns[1].key {
		t.Errorf("upstream sockets use keys %v; want the second client socket on the other account", keysOf(conns))
	}
	u.mu.Lock()
	posts := len(u.posts)
	u.mu.Unlock()
	if posts != 0 {
		t.Errorf("upstream got %d HTTP requests, want none", posts)
	}
}

func keysOf(conns []wsUpstreamConn) []string {
	var ks []string
	for _, c := range conns {
		ks = append(ks, c.key)
	}
	return ks
}

// On an account without the upstream WebSocket every turn is an HTTP request carrying the
// whole conversation: a continuation gets the previous request's input and the previous
// response's output ahead of its own input, and no previous_response_id.
func TestResponsesWebSocketOverHTTPCarriesTheConversation(t *testing.T) {
	u := newWSUpstream(t)
	base := startWSCore(t, u, false, "sk-a")

	c := dialResponses(t, base)
	first := wsTurn(t, c, wsCreate("one", ""))
	second := wsTurn(t, c, wsCreate("two", responseID(first)))
	if first["type"] != "response.completed" || second["type"] != "response.completed" {
		t.Fatalf("turns ended with %v and %v", first, second)
	}
	if conns := u.connections(); len(conns) != 0 {
		t.Fatalf("upstream got %d WebSockets, want none", len(conns))
	}
	u.mu.Lock()
	posts := u.posts
	u.mu.Unlock()
	if len(posts) != 2 {
		t.Fatalf("upstream got %d HTTP requests, want 2", len(posts))
	}
	if _, ok := posts[1]["previous_response_id"]; ok {
		t.Errorf("second request has previous_response_id: %v", posts[1])
	}
	var texts []string
	input, _ := posts[1]["input"].([]any)
	for _, it := range input {
		item, _ := it.(map[string]any)
		content, _ := item["content"].([]any)
		for _, p := range content {
			part, _ := p.(map[string]any)
			texts = append(texts, fmt.Sprint(part["text"]))
		}
	}
	if strings.Join(texts, "|") != "one|reply 1|two" {
		t.Errorf("second request's input texts are %q, want one, reply 1, two", texts)
	}
}

// A client that goes away during a turn ends its session: core closes the session, and the
// Codex extension closes the upstream WebSocket.
func TestResponsesWebSocketDisconnectClosesUpstream(t *testing.T) {
	u := newWSUpstream(t)
	u.hold = make(chan struct{})
	defer close(u.hold)
	base := startWSCore(t, u, true, "sk-a")

	c := dialResponses(t, base)
	b, _ := json.Marshal(wsCreate("one", ""))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	if _, data, err := c.Read(ctx); err != nil || !strings.Contains(string(data), "response.created") {
		t.Fatalf("first message %q, %v; want response.created", data, err)
	}
	c.CloseNow()
	conns := u.connections()
	if len(conns) != 1 {
		t.Fatalf("upstream sockets: %d, want 1", len(conns))
	}
	select {
	case <-conns[0].closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream WebSocket stayed open after the client went away")
	}
}

// copyDir copies the files of dir (not its subfolders) into a new temporary folder.
func copyDir(t *testing.T, dir string) string {
	t.Helper()
	out := t.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, e.Name()), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// A reload that replaces the extension closes a socket's session once its turn has ended, since
// the session lives in the old extension process: a turn that continues it is refused with
// previous_response_not_found and the socket stays open; a turn with the whole conversation
// opens a session on the new extension.
func TestResponsesWebSocketSessionAcrossReload(t *testing.T) {
	u := newWSUpstream(t)
	config := func(dir string) map[string]any {
		return map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(dir, "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id":         "acct-a",
					"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": true},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-a"},
				}},
			},
		}
	}
	first, second := copyDir(t, filepath.Join(binDir, "codex")), copyDir(t, filepath.Join(binDir, "codex"))
	base, path, _ := runCoreArgs(t, config(first), noWatch...)

	c := dialResponses(t, base)
	one := wsTurn(t, c, wsCreate("one", ""))
	if one["type"] != "response.completed" {
		t.Fatalf("first turn ended with %v", one)
	}
	writeConfig(t, path, config(second))
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: status %d, %+v", code, s)
	}
	waitStatus(t, base, "only the new extension", func(s coreStatus) bool { return len(s.Extensions) == 1 })

	refused := wsTurn(t, c, wsCreate("two", responseID(one)))
	if e, _ := refused["error"].(map[string]any); refused["type"] != "error" || e["code"] != "previous_response_not_found" {
		t.Fatalf("continuation after the reload: %v; want previous_response_not_found", refused)
	}
	if m := wsTurn(t, c, wsCreate("three", "")); m["type"] != "response.completed" {
		t.Fatalf("full turn after the reload ended with %v", m)
	}
	if conns := u.connections(); len(conns) != 2 {
		t.Errorf("upstream sockets: %d, want 2 (one per extension process)", len(conns))
	}
}
