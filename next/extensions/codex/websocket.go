package main

// The Responses WebSocket (OpenAI's WebSocket mode): an account whose settings turn on
// "websockets" holds sessions, one upstream socket per session, as today's core does for a
// WebSocket client on a Codex credential with websockets enabled. The rules are in the protocol
// README (Sessions, openai.responses sessions).

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
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
const wsIdleTimeout = 5 * time.Minute

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
	err = s.relay(ctx, c, &reporting{Stream: out, tr: ur.tr})
	var e *llm.Error
	if errors.As(err, &e) && e.ProviderCode != openairesponses.CodePreviousResponseNotFound {
		// Today's core gives up a socket that reported an error: the next turn connects again.
		s.drop(c)
	}
	return err
}

// relay turns the upstream's messages into events until the turn's terminal message.
func (s *wsSession) relay(ctx context.Context, c *wsConn, out eventSender) error {
	dec := openairesponses.NewStreamDecoder()
	done := map[int]json.RawMessage{} // output_index -> finished item
	idle := time.NewTimer(wsIdleTimeout)
	defer idle.Stop()
	for {
		var data []byte
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			s.drop(c)
			return &llm.Error{Code: llm.CodeUnavailable, Status: http.StatusBadGateway, Scope: llm.ScopeEndpoint,
				ProviderCode: openairesponses.CodeUpstreamWebsocketLost,
				Message:      "the upstream WebSocket sent nothing for " + wsIdleTimeout.String()}
		case data = <-c.msgs:
		case <-c.ended:
			return lostError(c.err)
		}
		idle.Reset(wsIdleTimeout)
		events, err := dec.Decode(repair(data, done))
		if err != nil {
			return frameError(data, classify(err))
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

// frameError gives an upstream error message its HTTP status, which the WebSocket carries in
// the message ("status"), where an HTTP reply carries it in the response.
func frameError(data []byte, err error) error {
	var e *llm.Error
	if !errors.As(err, &e) {
		return err
	}
	var f struct {
		Status     int `json:"status"`
		StatusCode int `json:"status_code"`
	}
	_ = json.Unmarshal(data, &f)
	status := f.Status
	if status == 0 {
		status = f.StatusCode
	}
	if status < 400 || status > 599 {
		return e
	}
	if e.Status == 0 {
		e.Status = status
	}
	if e.Code == llm.CodeUpstream {
		switch {
		case status == http.StatusUnauthorized:
			e.Code, e.Scope = llm.CodeAuth, llm.ScopeCredential
		case status == http.StatusForbidden:
			e.Code, e.Scope = llm.CodePermission, llm.ScopeCredential
		case status == http.StatusTooManyRequests:
			e.Code, e.Scope = llm.CodeRateLimit, llm.ScopeCredential
		case status < 500:
			e.Code, e.Scope = llm.CodeInvalidRequest, llm.ScopeRequest
		}
	}
	return e
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
	if ur.sessionID != "" {
		// Today's core names the session in both, with these spellings.
		h["session_id"] = []string{ur.sessionID}
		h["Conversation_id"] = []string{ur.sessionID}
	}
	setClientHeaders(h, req, ur, s.set, a)
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
