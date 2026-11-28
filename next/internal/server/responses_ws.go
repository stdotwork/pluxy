package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type Session interface {
	AccountID() string
	Ended() bool
	Turn(ctx context.Context, account protocol.Account, req llm.Request) Stream
	Interrupt(raw []byte)
	Close()
}

type OpenSessionFunc func(ctx context.Context, extension string, account protocol.Account, info protocol.SessionInfo) (Session, *llm.Error)

const wsQueue = 16

const wsCloseReasonMax = 123

var codexOriginators = []string{"codex desktop", "codex-tui", "codex_cli_rs"}

func serveResponsesWebSocket(w http.ResponseWriter, r *http.Request, src Source) {
	g, release := src.Acquire()
	cfg := g.Config
	release()
	if e := refused(cfg, r, false); e != nil {
		responsesFormat.writeError(w, e)
		return
	}
	r, t := requestTrace(r)
	w.Header().Set(TraceHeader, t.id)
	if ts := r.Header.Get("X-Codex-Turn-State"); ts != "" {
		w.Header().Set("X-Codex-Turn-State", ts)
	}
	// a web page cannot set the key's header on a WebSocket.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover,
		InsecureSkipVerify: len(cfg.ClientKeys) > 0})
	if err != nil {
		return
	}
	conn.SetReadLimit(MaxRequestBody)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s := &wsSocket{conn: conn, r: r, src: src, ctx: ctx, noSession: map[string]bool{}, codexClient: isCodexClient(r), trace: t}
	defer s.close()
	queue := make(chan wsMessage, wsQueue)
	go s.read(queue, cancel)
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-queue:
			if msg.epoch < s.epoch.Load() {
				if s.sendError(openairesponses.PreviousResponseNotFound()) != nil {
					return
				}
				continue
			}
			if !s.handle(msg.data) {
				return
			}
		}
	}
}

func isCodexClient(r *http.Request) bool {
	ua := strings.TrimSpace(r.Header.Get("User-Agent"))
	if strings.HasPrefix(ua, "Codex Desktop/") || strings.HasPrefix(ua, "codex-tui/") || ua == "codex_cli_rs" ||
		strings.HasPrefix(ua, "codex_cli_rs/") || strings.HasPrefix(ua, "codex_exec/") {
		return true
	}
	o := strings.ToLower(strings.TrimSpace(r.Header.Get("Originator")))
	for _, c := range codexOriginators {
		if o == c || strings.HasPrefix(o, c+"/") {
			return true
		}
	}
	return false
}

type wsSocket struct {
	conn        *websocket.Conn
	r           *http.Request
	src         Source
	ctx         context.Context
	codexClient bool
	trace       *trace

	session    Session
	sessionExt string
	noSession  map[string]bool
	transcript openairesponses.Transcript
	pinned     string
	lastModel  string
	lastMode   string

	epoch atomic.Uint64

	mu   sync.Mutex
	turn *wsTurn

	capture atomic.Pointer[capture]
}

type wsMessage struct {
	data  []byte
	epoch uint64
}

const (
	wsModeSession = "session"
	wsModeHTTP    = "http"
)

type wsTurn struct {
	mode        string
	session     Session
	responseID  string
	cancel      context.CancelFunc
	interrupted string
}

func (s *wsSocket) read(queue chan<- wsMessage, cancel context.CancelFunc) {
	defer cancel()
	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			return
		}
		var m struct {
			Type string `json:"type"`
		}
		if json.Valid(data) && json.Unmarshal(data, &m) == nil && m.Type == openairesponses.MessageInterrupt {
			s.interrupt(data)
			continue
		}
		select {
		case queue <- wsMessage{data: data, epoch: s.epoch.Load()}:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *wsSocket) close() {
	if s.session != nil {
		s.session.Close()
	}
	_ = s.conn.CloseNow()
}

func (s *wsSocket) send(data []byte) error {
	if c := s.capture.Load(); c != nil {
		_, _ = c.Write(data)
		_, _ = c.Write([]byte("\n"))
	}
	return s.conn.Write(s.ctx, websocket.MessageText, data)
}

func (s *wsSocket) sendError(e *llm.Error) error {
	status, body := openaichat.ErrorResponse(e)
	var b struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	obj := map[string]json.RawMessage(nil)
	if json.Unmarshal(e.Raw, &b) == nil && b.Error != nil && (e.ProviderCode != llm.ProviderContextLengthExceeded ||
		string(b.Error["code"]) == `"`+llm.ProviderContextLengthExceeded+`"`) {
		obj = b.Error
	} else if b.Error = nil; json.Unmarshal(body, &b) == nil {
		obj = b.Error
		if e.ProviderCode == "" {
			delete(obj, "code")
		}
	}
	if e.Explain != nil && obj != nil {
		obj["pluxy"], _ = json.Marshal(s.trace.explained(e).Explain)
	}
	m := map[string]any{"type": "error", "status": status, "error": obj}
	if u := e.UsageLimit; u != nil {
		h := map[string]string{}
		for _, kv := range u.Headers() {
			h[kv[0]] = kv[1]
		}
		m["headers"] = h
	}
	if e == errClientKey {
		m["headers"] = map[string]string{"X-Should-Retry": "false"}
	}
	msg, _ := json.Marshal(m)
	return s.send(msg)
}

func (s *wsSocket) refuseTurn(t *trace, e *llm.Error) bool {
	if t != nil && e.Code != llm.CodeCancelled {
		t.failed = true
	}
	return s.sendError(e) == nil
}

func clientError(status int, msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: status, Scope: llm.ScopeRequest, Message: msg}
}

func (s *wsSocket) interrupt(data []byte) {
	g, release := s.src.Acquire()
	e := refused(g.Config, s.r, false)
	release()
	if e != nil {
		_ = s.sendError(e)
		return
	}
	var m struct {
		ResponseID any `json:"response_id"`
	}
	_ = json.Unmarshal(data, &m)
	id, _ := m.ResponseID.(string)
	if strings.TrimSpace(id) == "" {
		_ = s.sendError(clientError(http.StatusBadRequest, "response.interrupt requires response_id"))
		return
	}
	s.mu.Lock()
	t := s.turn
	switch {
	case t == nil:
		s.mu.Unlock()
		_ = s.sendError(clientError(http.StatusBadRequest, "no active upstream websocket for response.interrupt"))
		return
	case t.mode == wsModeSession:
		known := t.responseID
		sess := t.session
		s.mu.Unlock()
		if known != "" && known != id {
			_ = s.sendError(clientError(http.StatusBadRequest, "response.interrupt does not target the active upstream response"))
			return
		}
		sess.Interrupt(data)
		return
	default:
		if t.interrupted == "" {
			t.interrupted = id
			t.cancel()
		}
		s.mu.Unlock()
	}
}

func (s *wsSocket) handle(data []byte) bool {
	g, release := s.src.Acquire()
	defer release()
	if e := refused(g.Config, s.r, false); e != nil {
		return s.sendError(e) == nil
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(data, &msg) != nil || msg == nil {
		msg = map[string]json.RawMessage{}
	}
	typ := jsonString(msg["type"])
	prev := jsonString(msg["previous_response_id"])
	refuse := func(e *llm.Error) bool { return s.sendError(e) == nil }
	switch typ {
	case openairesponses.MessageCreate, openairesponses.MessageAppend:
	default:
		return refuse(clientError(http.StatusBadRequest, "unsupported websocket request type: "+typ))
	}
	started := s.lastMode != ""
	continues := prev != "" || typ == openairesponses.MessageAppend
	switch {
	case typ == openairesponses.MessageAppend && !started && !s.transcript.Started():
		return refuse(clientError(http.StatusBadRequest, "websocket request received before response.create"))
	case prev != "" && !started && !s.transcript.Started():
		return refuse(openairesponses.PreviousResponseNotFound())
	}
	if in, ok := msg["input"]; ok && !isJSONArray(in) {
		return refuse(clientError(http.StatusBadRequest, "websocket request requires array field: input"))
	}
	model := jsonString(msg["model"])
	if model == "" {
		model = s.lastModel
	}
	if model == "" {
		return refuse(clientError(http.StatusBadRequest, "missing model in response.create request"))
	}

	if continues && s.lastMode == wsModeSession {
		if s.session == nil || s.session.Ended() {
			return refuse(openairesponses.PreviousResponseNotFound())
		}
		return s.runTurn(g, msg, model, wsModeSession)
	}
	if continues {
		return s.runTurn(g, msg, model, wsModeHTTP)
	}
	return s.runTurn(g, msg, model, "")
}

func jsonString(v json.RawMessage) string {
	var s string
	_ = json.Unmarshal(v, &s)
	return strings.TrimSpace(s)
}

func isJSONArray(v json.RawMessage) bool {
	v = json.RawMessage(strings.TrimSpace(string(v)))
	return len(v) > 0 && v[0] == '['
}

func (s *wsSocket) runTurn(g *Generation, msg map[string]json.RawMessage, model, mode string) bool {
	arrived := time.Now()
	native := make(map[string]json.RawMessage, len(msg))
	for k, v := range msg {
		native[k] = v
	}
	native["model"], _ = json.Marshal(model)
	nativeBody, _ := json.Marshal(native)
	var turnTrace *trace
	if g.RequestLog != nil {
		turnLog := g.RequestLog.newCapture()
		turnTrace = newTrace()
		s.capture.Store(turnLog)
		defer func() {
			s.capture.Store(nil)
			g.RequestLog.write(&requestEntry{at: arrived, method: s.r.Method, url: s.r.URL, client: ClientIP(s.r), headers: s.r.Header, body: nativeBody,
				trace: turnTrace, reply: turnLog, replyTitle: "WEBSOCKET MESSAGES"})
		}()
	}
	sessReq, err := openairesponses.ParseRequest(nativeBody)
	if err != nil {
		return s.refuseTurn(turnTrace, asLLMError(err))
	}
	sessReq.DropPolicy = g.Config.DropPolicy
	takeThinkingSuffix(&sessReq)
	s.nativeHeaders(&sessReq)

	var httpTurn openairesponses.TranscriptTurn
	var httpReq llm.Request
	if mode != wsModeSession {
		var e *llm.Error
		if httpTurn, e = s.transcript.Next(native); e != nil {
			return s.refuseTurn(turnTrace, e)
		}
		if httpReq, err = openairesponses.ParseRequest(httpTurn.Body); err != nil {
			return s.refuseTurn(turnTrace, asLLMError(err))
		}
		markParsed(&httpReq)
		httpReq.DropPolicy = g.Config.DropPolicy
		takeThinkingSuffix(&httpReq)
		s.nativeHeaders(&httpReq)
	}

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	if g.RequestLog != nil && g.RequestLog.All {
		ctx = turnTrace.captureUpstream(ctx, g.RequestLog.Redact)
	}
	entry := g.Requests.beginTurn(s.r, g, s.trace.id, arrived, sessReq.Model, cancel)
	defer g.Requests.end(entry)
	turn := &wsTurn{cancel: cancel}
	var opened []Session
	var sess Session
	var sessExt string
	chosen := ""
	ranOn := ""
	attempted := func(m string, on Session, ext string) {
		chosen, sess, sessExt = m, on, ext
		if on != nil {
			ranOn = on.AccountID()
		}
		s.mu.Lock()
		turn.mode, turn.session, turn.responseID = m, on, ""
		s.mu.Unlock()
	}
	rr := route.Request{LLM: sessReq, Tenant: tenant(s.r), Session: sessionKey(s.r, nativeBody), Pin: strings.TrimSpace(s.r.Header.Get(PinHeader))}
	if rr.Pin == "" {
		rr.Pin = s.pinned
	}
	switch mode {
	case wsModeSession:
		if rr.Pin == "" || rr.Pin == s.pinned {
			rr.Pin = s.session.AccountID()
		}
		rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
			if ext != s.sessionExt || a.ID != s.session.AccountID() || s.session.Ended() {
				attempted("", nil, "")
				return failedTurn(openairesponses.PreviousResponseNotFound())
			}
			attempted(wsModeSession, s.session, ext)
			return s.session.Turn(ctx, a, req)
		}
	case wsModeHTTP:
		rr.LLM = httpReq
		rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
			attempted(wsModeHTTP, nil, "")
			return g.Attempt(ctx, ext, a, req)
		}
	default:
		rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
			if s.session != nil && !s.session.Ended() && s.sessionExt == ext && s.session.AccountID() == a.ID {
				attempted(wsModeSession, s.session, ext)
				return s.session.Turn(ctx, a, req)
			}
			key := ext + "\x00" + a.ID
			if g.OpenSession != nil && !s.noSession[key] {
				ns, e := g.OpenSession(ctx, ext, a, protocol.SessionInfo{Format: protocol.SessionOpenAIResponses})
				if e == nil {
					opened = append(opened, ns)
					attempted(wsModeSession, ns, ext)
					return ns.Turn(ctx, a, req)
				}
				if e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
					attempted("", nil, "")
					return failedTurn(e)
				}
				s.noSession[key] = true
			}
			attempted(wsModeHTTP, nil, "")
			if httpTurn.Warmup {
				return newWarmup(model)
			}
			send := route.Renamed(httpReq, req.Model)
			send.Caller = req.Caller
			return g.Attempt(ctx, ext, a, send)
		}
	}
	s.mu.Lock()
	s.turn = turn
	s.mu.Unlock()
	endTurn := func() {
		s.mu.Lock()
		s.turn = nil
		s.mu.Unlock()
	}
	defer endTurn()

	turnReq := sessReq
	turnReq.Stream = true
	m := g.newMeter(s.r, arrived, turnReq, clientToken(s.r, false))
	var attemptExt string
	rr.Attempting = func(a route.Attempt) {
		attemptExt = a.Extension
		m.attempt(a, "")
		entry.attempt(a)
		if turnTrace != nil {
			turnTrace.account = a.Account
		}
	}
	rr.AttemptFailed = m.attemptFailed
	stream := meterStream(g.Router.Generate(host.WithPayloadClient(ctx, sessReq.Model, s.r.Header), rr), m)
	defer stream.Close()
	ev := stream.Next()
	if sess != nil && sess != s.session {
		if s.session != nil {
			s.session.Close()
		}
		s.session, s.sessionExt = sess, sessExt
	}
	for _, o := range opened {
		if o != s.session {
			o.Close()
		}
	}

	req := sessReq
	if chosen == wsModeHTTP {
		req = httpReq
	}
	var output []json.RawMessage
	var completed json.RawMessage
	sw := openairesponses.NewMessageWriter(func(typ string, data []byte) error {
		switch typ {
		case "response.subscription_usage":
			return nil
		case "response.metadata":
			if !s.codexClient {
				return nil
			}
		case "response.created":
			var d struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			if json.Unmarshal(data, &d) == nil {
				s.mu.Lock()
				turn.responseID = d.Response.ID
				s.mu.Unlock()
			}
		case "response.output_item.done":
			var d struct {
				Item json.RawMessage `json:"item"`
			}
			if json.Unmarshal(data, &d) == nil && len(d.Item) > 0 {
				output = append(output, d.Item)
			}
		case "response.completed", "response.incomplete":
			var d struct {
				Response struct {
					Output json.RawMessage `json:"output"`
				} `json:"response"`
			}
			if json.Unmarshal(data, &d) == nil {
				completed = d.Response.Output
			}
			endTurn()
		case "response.failed":
			endTurn()
		}
		return s.send(data)
	}, req)
	sw.KeepOutput = attemptExt == legacy.Codex && liteTurn(sessReq)
	defer func() { logTranslation(s.r, nil, sw) }()
	if ev.Type != llm.EventError && chosen == wsModeHTTP && httpTurn.Warmup {
		m.drop()
	}
	for {
		if ev.Type == llm.EventError {
			s.mu.Lock()
			id := turn.interrupted
			s.mu.Unlock()
			if id == "" {
				if chosen == wsModeSession && ev.Error.ProviderCode == openairesponses.CodeUpstreamWebsocketLost {
					s.pinned = ranOn
					s.epoch.Add(1)
				}
				status, _ := openaichat.ErrorResponse(ev.Error)
				m.fail(ev.Error, status)
				if turnTrace != nil && ev.Error.Code != llm.CodeCancelled {
					turnTrace.failed = true
				}
				return s.turnFailed(ev.Error)
			}
			m.fail(ev.Error, http.StatusOK)
			if s.send(openairesponses.InterruptedMessage(id, output)) != nil {
				return false
			}
			completed = marshalItems(output)
			break
		}
		end, ahead := ev, opensWithEnd(ev)
		if ahead {
			if end = stream.Next(); end.Type == llm.EventError {
				ev = end
				continue
			}
		}
		if end.Type == llm.EventFinish {
			m.finish(end)
		}
		if err := sw.Write(ev); err != nil {
			return false
		}
		if m != nil {
			m.sent = http.StatusOK
		}
		if ev.Terminal() {
			break
		}
		if ahead {
			ev = end
		} else {
			ev = stream.Next()
		}
	}
	s.lastModel = model
	switch {
	case chosen == wsModeSession:
		s.lastMode = wsModeSession
		s.pinned = ranOn
		s.transcript.Reset()
	case httpTurn.Warmup:
		s.lastMode = wsModeHTTP
		s.transcript.Warmup(httpTurn.Body, warmupID(ev))
	default:
		s.lastMode = wsModeHTTP
		if completed == nil {
			completed = json.RawMessage("[]")
		}
		s.transcript.Commit(httpTurn.Body, completed)
	}
	return true
}

func opensWithEnd(ev llm.Event) bool {
	if ev.Type != llm.EventResponseStart || ev.Response == nil || ev.Response.Format != llm.FormatOpenAIResponses {
		return false
	}
	var d struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(ev.Raw, &d)
	return d.Type == "response.completed" || d.Type == "response.incomplete"
}

func liteTurn(req llm.Request) bool {
	if req.Native == nil {
		return false
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(req.Native.Body, &m)
	return openairesponses.LiteRequest(req.Native.Headers, m)
}

func marshalItems(items []json.RawMessage) json.RawMessage {
	if items == nil {
		items = []json.RawMessage{}
	}
	b, _ := json.Marshal(items)
	return b
}

func (s *wsSocket) nativeHeaders(req *llm.Request) {
	if req.Native == nil {
		return
	}
	for _, h := range responsesFormat.headers {
		if v := s.r.Header.Values(h); len(v) > 0 {
			if req.Native.Headers == nil {
				req.Native.Headers = map[string]string{}
			}
			req.Native.Headers[h] = strings.Join(v, ",")
		}
	}
}

func (s *wsSocket) turnFailed(e *llm.Error) bool {
	if e.Code == llm.CodeCancelled {
		return false
	}
	logFailure(s.r, e)
	switch e.ProviderCode {
	case openairesponses.CodeUpstreamWebsocketLost, openairesponses.CodePreviousResponseNotFound:
		if e.Code != llm.CodeInvalidRequest || e.ProviderCode == openairesponses.CodePreviousResponseNotFound {
			return s.sendError(e) == nil
		}
	case openairesponses.CodeMessageTooBig:
		_ = s.conn.Close(websocket.StatusMessageTooBig, cutReason(e.Message))
		return false
	}
	if exposed(e) {
		_ = s.sendError(e)
	}
	return false
}

func exposed(e *llm.Error) bool {
	switch e.Status {
	case 401, 402, 403, 429:
		return false
	case 400, 409, 413, 422:
		return true
	}
	var b struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	return e.Status >= 400 && json.Unmarshal(e.Raw, &b) == nil && b.Error.Type == "invalid_request_error"
}

func cutReason(s string) string {
	if len(s) <= wsCloseReasonMax {
		return s
	}
	cut := wsCloseReasonMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func failedTurn(e *llm.Error) route.Stream {
	return &eventsStream{evs: []llm.Event{{Type: llm.EventError, Error: e}}}
}

type eventsStream struct {
	evs []llm.Event
	i   int
}

func (w *eventsStream) Next() llm.Event {
	ev := w.evs[w.i]
	if w.i < len(w.evs)-1 {
		w.i++
	}
	return ev
}

func (w *eventsStream) Close() {}

func newWarmup(model string) route.Stream {
	id := "resp_prewarm_" + rand.Text()
	created := time.Now().Unix()
	msgs := openairesponses.WarmupMessages(id, model, created)
	return &eventsStream{evs: []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: id, Model: model, Created: created, Format: llm.FormatOpenAIResponses}, Raw: msgs[0]},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop, RawReason: "completed"}, Raw: msgs[1]},
	}}
}

func warmupID(ev llm.Event) string {
	var d struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	_ = json.Unmarshal(ev.Raw, &d)
	return d.Response.ID
}
