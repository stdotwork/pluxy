package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// An account takes a session only with "websockets": true; others answer unsupported on the
// credential, so core sends their turns as generate calls.
func TestOpenSessionNeedsWebsockets(t *testing.T) {
	key := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"}
	for _, c := range []struct {
		settings string
		format   string
		scope    string
	}{
		{`{}`, protocol.SessionOpenAIResponses, llm.ScopeCredential},
		{`{"websockets":false}`, protocol.SessionOpenAIResponses, llm.ScopeCredential},
		{`{"websockets":true}`, "other.kind", llm.ScopeRequest},
	} {
		_, err := openSession(context.Background(), sdk.SessionCall{Format: c.format,
			Account: protocol.Account{ID: "a", Settings: json.RawMessage(c.settings), Credential: key}})
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != c.scope {
			t.Errorf("settings %s, kind %s: %v; want unsupported on the %s", c.settings, c.format, err, c.scope)
		}
	}
	s, err := openSession(context.Background(), sdk.SessionCall{Format: protocol.SessionOpenAIResponses,
		Account: protocol.Account{ID: "a", Settings: json.RawMessage(`{"websockets":true}`), Credential: key}})
	if err != nil {
		t.Fatalf("websockets on: %v", err)
	}
	s.Close()
}

// A socket that ends during a turn: closed with 1009 the message was too big; lost without a
// close message the turn's outcome is unknown; another close code is an upstream failure.
func TestLostError(t *testing.T) {
	var e *llm.Error
	if err := lostError(websocket.CloseError{Code: websocket.StatusMessageTooBig, Reason: "frame too large"}); !errors.As(err, &e) ||
		e.ProviderCode != openairesponses.CodeMessageTooBig || e.Message != "frame too large" || e.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("1009: %+v", err)
	}
	for _, err := range []error{websocket.CloseError{Code: websocket.StatusAbnormalClosure}, io.EOF} {
		if got := lostError(err); !errors.As(got, &e) || e.ProviderCode != openairesponses.CodeUpstreamWebsocketLost || e.Scope != llm.ScopeRequest {
			t.Errorf("%v: %+v; want upstream_websocket_lost on the request", err, got)
		}
	}
	if err := lostError(websocket.CloseError{Code: websocket.StatusInternalError}); !errors.As(err, &e) ||
		e.Code != llm.CodeUpstream || e.ProviderCode != "" {
		t.Errorf("1011: %+v; want an upstream failure", err)
	}
}

// An error message carries its HTTP status in the message; it decides the error's code.
func TestFrameErrorStatus(t *testing.T) {
	for status, code := range map[int]string{
		http.StatusUnauthorized:    llm.CodeAuth,
		http.StatusTooManyRequests: llm.CodeRateLimit,
		http.StatusBadRequest:      llm.CodeInvalidRequest,
		http.StatusBadGateway:      llm.CodeUpstream,
	} {
		data, _ := json.Marshal(map[string]any{"type": "error", "status": status, "error": map[string]any{"message": "no"}})
		var e *llm.Error
		if err := frameError(data, &llm.Error{Code: llm.CodeUpstream, Message: "no"}); !errors.As(err, &e) || e.Code != code || e.Status != status {
			t.Errorf("status %d: %+v; want code %s", status, err, code)
		}
	}
}

// fakeWS is an upstream Responses WebSocket that records each handshake and answers each
// message with the given replies.
type fakeWS struct {
	*httptest.Server
	mu       sync.Mutex
	headers  []http.Header
	received [][]byte
	replies  func(conn *websocket.Conn, msg []byte) // nil: answer nothing
}

func newFakeWS(t *testing.T) *fakeWS {
	f := &fakeWS{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, msg, err := c.Read(context.Background())
			if err != nil {
				return
			}
			f.mu.Lock()
			f.received = append(f.received, msg)
			replies := f.replies
			f.mu.Unlock()
			if replies != nil {
				replies(c, msg)
			}
		}
	}))
	t.Cleanup(f.Close)
	return f
}

type collected struct{ events []llm.Event }

func (c *collected) Send(ev llm.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func wsSessionFor(t *testing.T, f *fakeWS, cred *protocol.Credential) (*wsSession, protocol.Account) {
	t.Helper()
	acct := protocol.Account{ID: "a", Settings: json.RawMessage(`{"websockets":true,"base_url":"` + f.URL + `"}`), Credential: cred}
	s, err := openSession(context.Background(), sdk.SessionCall{Format: protocol.SessionOpenAIResponses, Account: acct})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s.(*wsSession), acct
}

func wsRequest(t *testing.T, body string) llm.Request {
	t.Helper()
	req, err := openairesponses.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// A subscription's socket is opened with its access token, its ChatGPT account and the routing
// hint, and the WebSocket beta and session headers.
func TestSubscriptionSocketHandshake(t *testing.T) {
	f := newFakeWS(t)
	cred := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "access-test", Data: json.RawMessage(`{"account_id":"chatgpt-acct"}`)}
	s, acct := wsSessionFor(t, f, cred)
	a, err := accountOf(cred)
	if err != nil {
		t.Fatal(err)
	}
	req := wsRequest(t, `{"model":"gpt-test","prompt_cache_key":"cache-1","input":[]}`)
	ur, err := buildRequest(req, acct.ID, s.set, a, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.dial(context.Background(), req, ur, a); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	h := f.headers[0]
	f.mu.Unlock()
	for name, want := range map[string]string{
		"Authorization":        "Bearer access-test",
		"Openai-Beta":          openAIBeta,
		"Chatgpt-Account-Id":   "chatgpt-acct",
		"X-Codex-Routing-Hint": "model=gpt-test",
		"Session_id":           "cache-1",
		"Conversation_id":      "cache-1",
		"Accept-Encoding":      "",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// A turn that continues a response when the session has no socket (never had one, or lost it)
// is refused with previous_response_not_found, without connecting.
func TestContinuationWithoutSocket(t *testing.T) {
	f := newFakeWS(t)
	s, acct := wsSessionFor(t, f, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"})
	req := wsRequest(t, `{"model":"gpt-test","previous_response_id":"resp_1","input":[]}`)
	var e *llm.Error
	if err := s.Turn(context.Background(), sdk.Call{Account: acct, Request: req}, nil); !errors.As(err, &e) ||
		e.ProviderCode != openairesponses.CodePreviousResponseNotFound {
		t.Fatalf("turn: %v; want previous_response_not_found", err)
	}
	f.mu.Lock()
	n := len(f.headers)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("the upstream got %d handshakes, want none", n)
	}
}

// During a turn an upstream silent for the liveness timeout counts as lost: the turn fails with
// upstream_websocket_lost and the socket is given up, so a continuation is then refused.
func TestSilentUpstreamIsLost(t *testing.T) {
	old := wsIdleTimeout
	wsIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { wsIdleTimeout = old })
	f := newFakeWS(t)
	s, acct := wsSessionFor(t, f, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"})
	a, _ := accountOf(acct.Credential)
	req := wsRequest(t, `{"model":"gpt-test","input":[]}`)
	ur, err := buildRequest(req, acct.ID, s.set, a, true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.dial(context.Background(), req, ur, a)
	if err != nil {
		t.Fatal(err)
	}
	turn := make(chan struct{})
	c.mu.Lock()
	c.turn = turn
	c.mu.Unlock()
	defer close(turn)
	var e *llm.Error
	if err := s.relay(context.Background(), c, &collected{}); !errors.As(err, &e) || e.ProviderCode != openairesponses.CodeUpstreamWebsocketLost ||
		e.Scope != llm.ScopeRequest {
		t.Fatalf("relay: %+v; want upstream_websocket_lost on the request, which puts no cooldown on the account", err)
	}
	if s.current() != nil {
		t.Error("the session kept the silent socket")
	}
}

// The upstream's events come back as the turn's events, and a later message on the socket
// is relayed for the turn that waits for it.
func TestRelayEvents(t *testing.T) {
	f := newFakeWS(t)
	f.replies = func(c *websocket.Conn, _ []byte) {
		for _, m := range []string{
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-test","output":[]}}`,
			`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output":[]}}`,
		} {
			_ = c.Write(context.Background(), websocket.MessageText, []byte(m))
		}
	}
	s, acct := wsSessionFor(t, f, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"})
	a, _ := accountOf(acct.Credential)
	req := wsRequest(t, `{"model":"gpt-test","input":[]}`)
	ur, _ := buildRequest(req, acct.ID, s.set, a, true)
	c, err := s.dial(context.Background(), req, ur, a)
	if err != nil {
		t.Fatal(err)
	}
	turn := make(chan struct{})
	c.mu.Lock()
	c.turn = turn
	c.mu.Unlock()
	defer close(turn)
	if err := c.c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	out := &collected{}
	if err := s.relay(context.Background(), c, out); err != nil {
		t.Fatal(err)
	}
	if len(out.events) < 2 || out.events[0].Type != llm.EventResponseStart || out.events[0].Response.ID != "resp_1" ||
		!out.events[len(out.events)-1].Terminal() {
		t.Fatalf("events %+v; want resp_1's start through its end", out.events)
	}
}
