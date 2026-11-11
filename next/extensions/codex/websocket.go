package main

// The Responses WebSocket (OpenAI's WebSocket mode): an account whose settings turn on
// "websockets" holds sessions, one upstream socket per session, as today's core does for a
// WebSocket client on a Codex credential with websockets enabled. The rules are in the protocol
// README (Sessions, openai.responses sessions).

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// openAIBeta is the beta header the Responses WebSocket takes, as the Codex client sends it.
const openAIBeta = "responses_websockets=2026-02-06"

// wsIdleTimeout is the liveness check on the upstream socket: during a turn the upstream must
// send a message at least this often, or the socket counts as lost. It is the only timeout after
// the connection is made, as today's core's Codex WebSocket read deadline is.
var wsIdleTimeout = 5 * time.Minute // a variable for the tests

// wsDialTimeout bounds the handshake: connecting is not yet the upstream's work.
const wsDialTimeout = 30 * time.Second

// wsClient makes the handshakes. It speaks only HTTP/1.1, which a WebSocket upgrade needs, and
// like client it never follows a redirect to another host. It asks for no compressed handshake
// reply (no Accept-Encoding), as Codex's own client and today's core do; messages are compressed
// with permessage-deflate instead.
var wsClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		DisableCompression:  true,
	},
	CheckRedirect: client.CheckRedirect,
}

// openSession opens a Responses WebSocket session for an account whose settings turn the
// upstream WebSocket on; other accounts take none, and core sends their requests as generate
// calls.
func openSession(ctx context.Context, call sdk.SessionCall) (sdk.Session, error) {
	if call.Format != protocol.SessionOpenAIResponses {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeRequest, Message: "the Codex extension holds only openai.responses sessions"}
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if s.Websockets == nil || !*s.Websockets {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Message: "the account's upstream WebSocket is off (settings.websockets)"}
	}
	if _, err := accountOf(call.Account.Credential); err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(context.Background())
	return &wsSession{set: s, ctx: sctx, cancel: cancel}, nil
}

// wsSession is one session: at most one upstream socket at a time. A socket that is lost is
// not replaced until a turn that continues nothing needs one.
type wsSession struct {
	set    settings
	ctx    context.Context // ends with the session
	cancel context.CancelFunc

	mu   sync.Mutex
	conn *wsConn // nil before the first turn and after a loss
}

// wsConn is one upstream socket and its reader.
type wsConn struct {
	c     *websocket.Conn
	msgs  chan []byte   // messages of the running turn
	ended chan struct{} // closed after the reader's last message, when the socket has ended
	err   error         // why it ended; set before ended is closed

	mu   sync.Mutex
	turn chan struct{} // closed when the running turn stops reading; nil between turns
}

// read reads every message of the socket: between turns too, so the socket's end is seen while
// it is idle. A message no turn waits for is dropped, as today's core drops it.
func (w *wsConn) read(ctx context.Context) {
	defer w.c.CloseNow() // after ended is closed: the peer sees the close only once the loss is known here
	defer close(w.ended)
	for {
		_, data, err := w.c.Read(ctx)
		if err != nil {
			w.err = err
			return
		}
		w.mu.Lock()
		turn := w.turn
		w.mu.Unlock()
		if turn == nil {
			continue
		}
		select {
		case w.msgs <- data:
		case <-turn:
		case <-ctx.Done():
			w.err = ctx.Err()
			return
		}
	}
}

func (w *wsConn) alive() bool {
	select {
	case <-w.ended:
		return false
	default:
		return true
	}
}

// current returns the session's socket while it is alive.
func (s *wsSession) current() *wsConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && !s.conn.alive() {
		s.conn = nil
	}
	return s.conn
}

// drop forgets c and closes it: the responses it held are gone.
func (s *wsSession) drop(c *wsConn) {
	s.mu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.mu.Unlock()
	_ = c.c.CloseNow()
}

// Turn sends one response.create on the session's socket, connecting first when there is none,
// and relays the upstream's messages until the turn ends.
func (s *wsSession) Turn(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return err
	}
	if a.siwc && codexClient(call.Request) {
		return siwcCodexClient()
	}
	ur, err := buildRequest(call.Request, call.Account.ID, s.set, a, true)
	if err != nil {
		return err
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(ur.body, &m)
	m["type"] = raw("response.create")
	var prev string
	_ = json.Unmarshal(m["previous_response_id"], &prev)
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}

	c := s.current()
	if c == nil {
		if prev != "" {
			// The response it continues lived on a socket that is gone (or never was).
			return openairesponses.PreviousResponseNotFound()
		}
		if c, err = s.dial(ctx, call.Request, ur, a); err != nil {
			return err
		}
	}
	turn := make(chan struct{})
	c.mu.Lock()
	c.turn = turn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.turn = nil
		c.mu.Unlock()
		close(turn)
	}()
	if err := c.c.Write(ctx, websocket.MessageText, body); err != nil {
		s.drop(c)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return openairesponses.UpstreamWebsocketLost()
	}
	err = s.relay(ctx, c, renamer{&reporting{Stream: out, tr: ur.tr}, ur.names}, ur.repairs())
	var e *llm.Error
	if errors.As(err, &e) && e.ProviderCode != openairesponses.CodePreviousResponseNotFound {
		// Today's core gives up a socket that reported an error: the next turn connects again.
		s.drop(c)
	}
	return err
}

// relay turns the upstream's messages into events until the turn's terminal message.
func (s *wsSession) relay(ctx context.Context, c *wsConn, out eventSender, rep *repairs) error {
	dec := openairesponses.NewStreamDecoder()
	idle := time.NewTimer(wsIdleTimeout)
	defer idle.Stop()
	for {
		var data []byte
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			// An upstream silent this long is taken as lost: the client hears so and may send
			// the whole conversation again.
			s.drop(c)
			return openairesponses.UpstreamWebsocketLost()
		case data = <-c.msgs:
		case <-c.ended:
			return lostError(c.err)
		}
		idle.Reset(wsIdleTimeout)
		repaired, err := rep.apply(data)
		if err != nil {
			return err
		}
		events, err := dec.Decode(repaired)
		if err != nil {
			return frameError(data, classify(failedFirst(events, err)))
		}
		for _, ev := range events {
			if err := out.Send(ev); err != nil {
				return err
			}
			if ev.Terminal() {
				return nil
			}
		}
	}
}

// lostError is the error for a socket that ended during a turn: closed with 1009 (a message was
// too big), or lost some other way.
func lostError(err error) error {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.StatusMessageTooBig:
			return openairesponses.MessageTooBig(ce.Reason)
		case websocket.StatusAbnormalClosure, websocket.StatusNoStatusRcvd:
		default:
			return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint,
				Message: "the upstream closed its WebSocket: " + ce.Error()}
		}
	}
	return openairesponses.UpstreamWebsocketLost()
}

// frameError gives an upstream error message the meaning its HTTP status has, as today's core
// does: the WebSocket carries in the message ("status", "headers", and the error in "error" or
// "body.error") what an HTTP reply carries in its response, so the message is read as that
// reply would be (statusError), whatever its body's error type says. Only an error message is read
// this way, as today's core reads it: a response.failed keeps what its response.error means (err),
// whatever status it carries, and so does a message without a status.
func frameError(data []byte, err error) error {
	var e *llm.Error
	if !errors.As(err, &e) {
		return err
	}
	var f struct {
		Type       string                     `json:"type"`
		Status     int                        `json:"status"`
		StatusCode int                        `json:"status_code"`
		Headers    map[string]json.RawMessage `json:"headers"`
		Error      json.RawMessage            `json:"error"`
		Body       struct {
			Error json.RawMessage `json:"error"`
		} `json:"body"`
	}
	_ = json.Unmarshal(data, &f)
	status := f.Status
	if status == 0 {
		status = f.StatusCode
	}
	if f.Type != "error" || status < 400 || status > 599 {
		return e
	}
	errObj := f.Body.Error
	if len(errObj) == 0 {
		errObj = f.Error
	}
	if len(errObj) == 0 {
		errObj, _ = json.Marshal(map[string]string{"type": "server_error", "message": http.StatusText(status)})
	}
	body, _ := json.Marshal(map[string]json.RawMessage{"error": errObj})
	h := http.Header{}
	for k, v := range f.Headers {
		var s string
		if json.Unmarshal(v, &s) != nil {
			s = string(v)
		}
		h.Set(k, s)
	}
	return statusError(&http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: h, Body: io.NopCloser(bytes.NewReader(body))}, "")
}

// dial connects the session's socket to {base_url}/responses.
func (s *wsSession) dial(ctx context.Context, req llm.Request, ur *upstreamRequest, a account) (*wsConn, error) {
	base := s.set.BaseURL
	if base == "" {
		base = defaultAPIBaseURL
		if a.subscription {
			base = defaultSubscriptionBaseURL
		}
	}
	u := strings.TrimRight(base, "/") + "/responses"
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	default:
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeCredential, Message: "bad base_url: not an http or https URL"}
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+a.token)
	h.Set("OpenAI-Beta", openAIBeta)
	switch {
	case a.siwc:
		// As over HTTP, with the client's own session or request ID when the request has
		// none.
		var client map[string]string
		if req.Native != nil {
			client = req.Native.Headers
		}
		sid := ur.sessionID
		if sid == "" {
			sid = client["session-id"]
		}
		setSIWCHeaders(h, sid, s.set)
		if sid == "" && client["x-client-request-id"] != "" {
			h.Set("X-Client-Request-Id", client["x-client-request-id"])
		}
	default:
		if ur.sessionID != "" {
			// Today's core names the session in both, with these spellings.
			h["session_id"] = []string{ur.sessionID}
			h["Conversation_id"] = []string{ur.sessionID}
		}
		setClientHeaders(h, req, ur, s.set, a)
	}
	dctx, cancel := context.WithTimeout(ctx, wsDialTimeout)
	defer cancel()
	conn, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: wsClient, HTTPHeader: h,
		CompressionMode: websocket.CompressionNoContextTakeover})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, statusError(resp, ur.model)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var op *net.OpError
		dial := errors.As(err, &op) && op.Op == "dial"
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream WebSocket unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: dial}
	}
	conn.SetReadLimit(protocol.MaxFrame)
	c := &wsConn{c: conn, msgs: make(chan []byte), ended: make(chan struct{})}
	go c.read(s.ctx)
	s.mu.Lock()
	s.conn = c
	s.mu.Unlock()
	return c, nil
}

// Interrupt writes the client's control message to the turn's socket unchanged.
func (s *wsSession) Interrupt(raw json.RawMessage) {
	if c := s.current(); c != nil {
		if err := c.c.Write(s.ctx, websocket.MessageText, raw); err != nil {
			s.drop(c)
		}
	}
}

// Close closes the session's socket without a close message, as today's core does.
func (s *wsSession) Close() {
	s.cancel()
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		_ = c.c.CloseNow()
	}
}
