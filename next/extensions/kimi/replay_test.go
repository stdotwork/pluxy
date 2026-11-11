package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The assistant turn the fake Kimi answers the first request of a conversation with: signed
// thinking, then a tool call.
const (
	keptThinking = `{"type":"thinking","thinking":"The user wants Oslo's weather.","signature":"sig-oslo"}`
	keptToolUse  = `{"type":"tool_use","id":"toolu_oslo","name":"get_weather","input":{"city":"Oslo"}}`
)

// toolTurnSSE is that turn as Kimi's Messages API streams it.
var toolTurnSSE = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"k3","usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The user wants "}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Oslo's weather."}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-oslo"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_oslo","name":"get_weather","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Oslo\"}"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
	`{"type":"message_stop"}`,
}

// replayKimi is a fake Kimi whose Messages API answers each conversation's first request with
// the tool turn (streamed or not, as asked) and every later one with text, or with status when
// set.
type replayKimi struct {
	*kimitest.Server
	mu          sync.Mutex
	status      int
	streamError bool // a later request is answered with a stream that ends in an error event
}

func newReplayKimi(t *testing.T) *replayKimi {
	k := &replayKimi{Server: kimitest.New(t)}
	k.Generate = func(w http.ResponseWriter, r *http.Request, req kimitest.Request) {
		var in struct {
			Stream   bool              `json:"stream"`
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(req.Body, &in)
		k.mu.Lock()
		status, streamError := k.status, k.streamError
		k.mu.Unlock()
		switch {
		case streamError && len(in.Messages) > 1:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: "+toolTurnSSE[0]+"\n\n")
			io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"bad thinking\"}}\n\n")
		case status != 0:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad thinking"}}`)
		case len(in.Messages) > 1:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_2","type":"message","role":"assistant","model":"k3","content":[{"type":"text","text":"4°C"}],"stop_reason":"end_turn","usage":{"input_tokens":30,"output_tokens":3}}`)
		case in.Stream:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, d := range toolTurnSSE {
				var ev struct{ Type string }
				_ = json.Unmarshal([]byte(d), &ev)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, d)
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"k3","content":[`+keptThinking+`,`+keptToolUse+`],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":20}}`)
		}
	}
	return k
}

func (k *replayKimi) refuse(status int) {
	k.mu.Lock()
	k.status = status
	k.mu.Unlock()
}

// lastAssistant returns the content of the last assistant message of the last request the fake
// got.
func (k *replayKimi) lastAssistant(t *testing.T) string {
	t.Helper()
	reqs := k.Requests()
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
		t.Fatal(err)
	}
	for i := len(body.Messages) - 1; i >= 0; i-- {
		if body.Messages[i].Role == "assistant" {
			return string(body.Messages[i].Content)
		}
	}
	t.Fatal("no assistant message in the request")
	return ""
}

type turnCall struct {
	caller, session, userID, model string
	stream                         bool
}

// messages sends a Messages request with the given messages and reads the reply to its end.
func messages(t *testing.T, ext *host.Extension, k *replayKimi, c turnCall, msgs string) llm.Event {
	t.Helper()
	if c.model == "" {
		c.model = "kimi-k3"
	}
	body := fmt.Sprintf(`{"model":%q,"max_tokens":256,"stream":%v,"messages":%s`, c.model, c.stream, msgs)
	if c.userID != "" {
		body += fmt.Sprintf(`,"metadata":{"user_id":%q}`, c.userID)
	}
	body += "}"
	headers := map[string]string{}
	if c.session != "" {
		headers["x-claude-code-session-id"] = c.session
	}
	access, _ := k.Grant()
	acct := protocol.Account{ID: "kimi", Settings: k.Settings(), Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access}}
	req := llm.Request{Model: c.model, Stream: c.stream, Caller: c.caller,
		Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(body), Headers: headers}}
	s := ext.Generate(context.Background(), acct, req)
	defer s.Close()
	for {
		if ev := s.Next(); ev.Terminal() {
			return ev
		}
	}
}

const firstTurn = `[{"role":"user","content":"Weather in Oslo?"}]`

// secondTurn resends the tool turn with assistant content (its thinking left out, or not) and
// the tool's result.
func secondTurn(assistant string) string {
	return `[{"role":"user","content":"Weather in Oslo?"},{"role":"assistant","content":[` + assistant + `]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_oslo","content":"4°C"}]}]`
}

func sameContent(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if json.Unmarshal([]byte(got), &g) != nil || json.Unmarshal([]byte(want), &w) != nil || !reflect.DeepEqual(g, w) {
		t.Errorf("assistant content sent to Kimi:\n got %s\nwant %s", got, want)
	}
}

// A Messages client that resends an assistant tool turn without its signed thinking gets the
// thinking back, when the request comes from the same caller, in the same conversation and model
// family, and the rest of the turn is as Kimi sent it; any other request goes as it was written.
func TestThinkingReplay(t *testing.T) {
	ext, err := host.Start(conformance.BuildExtension(t, "pluxy-extension.json", "."), host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	stripped := secondTurn(keptToolUse)
	full := "[" + keptThinking + "," + keptToolUse + "]"
	asSent := "[" + keptToolUse + "]"

	for _, tc := range []struct {
		name         string
		first, again turnCall
		resend       string // the second request's messages; default stripped
		want         string // the assistant content Kimi must get
	}{
		{name: "streamed reply", first: turnCall{caller: "c1", session: "s-stream", stream: true}, again: turnCall{caller: "c1", session: "s-stream"}, want: full},
		{name: "whole reply", first: turnCall{caller: "c1", session: "s-whole"}, again: turnCall{caller: "c1", session: "s-whole", stream: true}, want: full},
		{name: "session in metadata.user_id", first: turnCall{caller: "c1", userID: "user_ab_account_cd_session_0f1e-2d3c", stream: true},
			again: turnCall{caller: "c1", userID: `{"device_id":"d","session_id":"0f1e-2d3c"}`}, want: full},
		{name: "same model family", first: turnCall{caller: "c1", session: "s-family", model: "kimi-k3", stream: true}, again: turnCall{caller: "c1", session: "s-family", model: "k3-256k"}, want: full},
		{name: "another caller", first: turnCall{caller: "c1", session: "s-caller", stream: true}, again: turnCall{caller: "c2", session: "s-caller"}, want: asSent},
		{name: "another session", first: turnCall{caller: "c1", session: "s-a", stream: true}, again: turnCall{caller: "c1", session: "s-b"}, want: asSent},
		{name: "another model family", first: turnCall{caller: "c1", session: "s-model", model: "kimi-k3", stream: true}, again: turnCall{caller: "c1", session: "s-model", model: "kimi-k2.8"}, want: asSent},
		{name: "no caller", first: turnCall{session: "s-anon", stream: true}, again: turnCall{session: "s-anon"}, want: asSent},
		{name: "no session", first: turnCall{caller: "c1", stream: true}, again: turnCall{caller: "c1"}, want: asSent},
		{name: "turn changed", first: turnCall{caller: "c1", session: "s-changed", stream: true}, again: turnCall{caller: "c1", session: "s-changed"},
			resend: secondTurn(strings.Replace(keptToolUse, "Oslo", "Bergen", 1)), want: strings.Replace(asSent, "Oslo", "Bergen", 1)},
		{name: "thinking kept by the client", first: turnCall{caller: "c1", session: "s-kept", stream: true}, again: turnCall{caller: "c1", session: "s-kept"},
			resend: secondTurn(`{"type":"thinking","thinking":"Mine.","signature":"sig-mine"},` + keptToolUse), want: `[{"type":"thinking","thinking":"Mine.","signature":"sig-mine"},` + keptToolUse + `]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newReplayKimi(t)
			if ev := messages(t, ext, k, tc.first, firstTurn); ev.Type != llm.EventFinish {
				t.Fatalf("first request: %+v", ev.Error)
			}
			resend := tc.resend
			if resend == "" {
				resend = stripped
			}
			if ev := messages(t, ext, k, tc.again, resend); ev.Type != llm.EventFinish {
				t.Fatalf("second request: %+v", ev.Error)
			}
			sameContent(t, k.lastAssistant(t), tc.want)
		})
	}

	// Kimi refusing a request whose thinking was put back drops what was kept: the next request
	// goes as written.
	t.Run("refused", func(t *testing.T) {
		k := newReplayKimi(t)
		c := turnCall{caller: "c1", session: "s-refused", stream: true}
		messages(t, ext, k, c, firstTurn)
		k.refuse(http.StatusBadRequest)
		if ev := messages(t, ext, k, c, stripped); ev.Type != llm.EventError {
			t.Fatalf("refused request ended with %s", ev.Type)
		}
		sameContent(t, k.lastAssistant(t), full)
		k.refuse(0)
		messages(t, ext, k, c, stripped)
		sameContent(t, k.lastAssistant(t), asSent)
	})
	t.Run("refused in the stream", func(t *testing.T) {
		k := newReplayKimi(t)
		c := turnCall{caller: "c1", session: "s-refused-stream", stream: true}
		messages(t, ext, k, c, firstTurn)
		k.mu.Lock()
		k.streamError = true
		k.mu.Unlock()
		if ev := messages(t, ext, k, c, stripped); ev.Type != llm.EventError {
			t.Fatalf("refused request ended with %s", ev.Type)
		}
		sameContent(t, k.lastAssistant(t), full)
		k.mu.Lock()
		k.streamError = false
		k.mu.Unlock()
		messages(t, ext, k, c, stripped)
		sameContent(t, k.lastAssistant(t), asSent)
	})
}

// Kept turns expire an hour after their last use, and the cache drops the least recently used
// turns beyond its entry and size bounds.
func TestReplayCacheBounds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newReplayCache()
	c.now = func() time.Time { return now }
	c.put("a", []byte("aaaa"))
	now = now.Add(59 * time.Minute)
	if _, ok := c.get("a"); !ok {
		t.Fatal("a turn was gone after 59 minutes")
	}
	now = now.Add(59 * time.Minute) // 118 minutes after it was kept, 59 after its last use
	if _, ok := c.get("a"); !ok {
		t.Fatal("a turn used 59 minutes ago was gone")
	}
	now = now.Add(time.Hour)
	if _, ok := c.get("a"); ok {
		t.Fatal("a turn unused for an hour was still there")
	}

	c.maxEntries, c.maxBytes = 2, 10
	c.put("1", []byte("1111"))
	c.put("2", []byte("2222"))
	c.get("1") // 2 is now the least recently used
	c.put("3", []byte("33"))
	if _, ok := c.get("2"); ok {
		t.Error("the least recently used turn stayed past the entry bound")
	}
	c.maxEntries = 3
	c.put("4", []byte("444444")) // 12 bytes with 1 and 3
	if _, ok := c.get("1"); ok {
		t.Error("the least recently used turn stayed past the size bound")
	}
	if _, ok := c.get("4"); !ok || c.bytes > c.maxBytes || len(c.entries) != c.order.Len() {
		t.Errorf("after eviction: bytes %d, %d entries, %d in order", c.bytes, len(c.entries), c.order.Len())
	}
	if c.put("big", make([]byte, replayMaxEntryBytes+1)); c.entries["big"] != nil {
		t.Error("a turn over the entry size bound was kept")
	}

	// Keys count toward the size bound: a client chooses its session ID, so turns with long keys
	// and little content cannot grow the cache past it.
	c = newReplayCache()
	c.now = func() time.Time { return now }
	c.maxEntries, c.maxBytes = 100, 64
	for i := range 10 {
		c.put(fmt.Sprintf("%030d", i), []byte("x"))
	}
	if c.bytes > c.maxBytes || len(c.entries) != 2 {
		t.Errorf("long keys: bytes %d, %d entries; want at most %d bytes, 2 entries", c.bytes, len(c.entries), c.maxBytes)
	}
	if c.put(strings.Repeat("k", replayMaxEntryBytes), []byte("x")); len(c.entries) != 2 {
		t.Error("a turn whose key is over the entry size bound was kept")
	}
}

// A stream the capture cannot rebuild exactly is not kept: an unknown delta, an error event, or
// one that never completes.
func TestReplayCaptureGivesUp(t *testing.T) {
	run := func(events []string) bool {
		c := newReplayCapture()
		for _, e := range events {
			c.observe([]byte(e))
		}
		_, ok := c.content()
		return ok
	}
	if !run(toolTurnSSE) {
		t.Fatal("the whole tool turn was not captured")
	}
	unknown := append(append([]string{}, toolTurnSSE[:3]...), `{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta"}}`)
	unknown = append(unknown, toolTurnSSE[3:]...)
	errored := append(append([]string{}, toolTurnSSE[:len(toolTurnSSE)-1]...), `{"type":"error","error":{"type":"overloaded_error"}}`, `{"type":"message_stop"}`)
	for name, events := range map[string][]string{
		"unknown delta": unknown,
		"error event":   errored,
		"no stop":       toolTurnSSE[:len(toolTurnSSE)-1],
		"bad input":     append(append([]string{}, toolTurnSSE[:8]...), toolTurnSSE[9:]...),
	} {
		if run(events) {
			t.Errorf("%s: captured", name)
		}
	}
}
