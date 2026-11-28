package main

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

var wsIdleTimeout = 5 * time.Minute

const wsDialTimeout = 30 * time.Second

var wsClient = &http.Client{
	Transport: sdk.Upstream(&http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		DisableCompression:  true,
	}),
	CheckRedirect: client.CheckRedirect,
}

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

type wsSession struct {
	set    settings
	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	conn *wsConn
}

type wsConn struct {
	c     *websocket.Conn
	msgs  chan []byte
	ended chan struct{}
	err   error

	mu   sync.Mutex
	turn chan struct{}
}

func (w *wsConn) read(ctx context.Context) {
	defer w.c.CloseNow()
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

func (s *wsSession) current() *wsConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && !s.conn.alive() {
		s.conn = nil
	}
	return s.conn
}

func (s *wsSession) drop(c *wsConn) {
	s.mu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.mu.Unlock()
	_ = c.c.CloseNow()
}

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
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	body = sdk.ApplyPayload(ctx, body)
	var sent struct {
		Previous string `json:"previous_response_id"`
	}
	_ = json.Unmarshal(body, &sent)
	prev := sent.Previous

	c := s.current()
	if c == nil {
		if prev != "" {
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
		s.drop(c)
	}
	return err
}

func (s *wsSession) relay(ctx context.Context, c *wsConn, out eventSender, rep *repairs) error {
	dec := openairesponses.NewStreamDecoder()
	idleLimit := sdk.IdleTimeout(ctx, wsIdleTimeout)
	var idle *time.Timer
	var idleC <-chan time.Time
	if idleLimit > 0 {
		idle = time.NewTimer(idleLimit)
		defer idle.Stop()
		idleC = idle.C
	}
	accountID := ""
	if info, ok := sdk.UpstreamInfo(ctx); ok {
		accountID = info.AccountID
	}
	for {
		var data []byte
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idleC:
			sdk.LogTimeout("idle", accountID, idleLimit)
			s.drop(c)
			return openairesponses.UpstreamWebsocketLost()
		case data = <-c.msgs:
		case <-c.ended:
			return lostError(c.err)
		}
		if idle != nil {
			idle.Reset(idleLimit)
		}
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
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream WebSocket unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: sdk.Retryable(err)}
	}
	conn.SetReadLimit(protocol.MaxFrame)
	c := &wsConn{c: conn, msgs: make(chan []byte), ended: make(chan struct{})}
	go c.read(s.ctx)
	s.mu.Lock()
	s.conn = c
	s.mu.Unlock()
	return c, nil
}

func (s *wsSession) Interrupt(raw json.RawMessage) {
	if c := s.current(); c != nil {
		if err := c.c.Write(s.ctx, websocket.MessageText, raw); err != nil {
			s.drop(c)
		}
	}
}

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
