package server

// OpenAI Responses over WebSocket (GET /v1/responses upgraded, OpenAI's WebSocket mode). A
// client sends response.create (or response.append) messages and gets each turn's stream events
// back as text messages. Core pins the socket to one session on one account when the account's
// extension holds sessions (protocol 0.1): every turn goes to that session, so a turn can
// continue the previous response with previous_response_id. On an account that takes no
// sessions each turn is a separate generate call carrying the whole conversation, which core
// keeps (openairesponses.Transcript). What clients see follows today's core, which the corpus
// records: the refused messages and their errors, which upstream errors reach the client and
// which close the socket without a message, interrupts, warmups and lost upstream sockets.

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
	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Session is an open session on a provider extension.
type Session interface {
	AccountID() string
	// Ended reports whether the session is over: closed by core, or by the extension.
	Ended() bool
	Turn(ctx context.Context, account protocol.Account, req llm.Request) Stream
	// Interrupt passes the client's control message for the running turn.
	Interrupt(raw []byte)
	Close()
}

// OpenSessionFunc opens a session on extension for account. An error with code unsupported and
// scope credential means the account takes no sessions.
type OpenSessionFunc func(ctx context.Context, extension string, account protocol.Account, info protocol.SessionInfo) (Session, *llm.Error)

// wsQueue is how many client messages wait while a turn runs, as in today's core; the reader
// stops reading the socket while it is full.
const wsQueue = 16

// wsCloseReasonMax is the most bytes a close reason may have: a close message's payload is at
// most 125 bytes, two of which are the code.
const wsCloseReasonMax = 123

// codexOriginators are the Originator values of the Codex clients, which get response.metadata.
var codexOriginators = []string{"codex desktop", "codex-tui", "codex_cli_rs"}

func serveResponsesWebSocket(w http.ResponseWriter, r *http.Request, src Source) {
	g, release := src.Acquire()
	cfg := g.Config
	release()
	if e := refused(cfg, r, false); e != nil {
		responsesFormat.writeError(w, e)
		return
	}
	if ts := r.Header.Get("X-Codex-Turn-State"); ts != "" {
		// Keeps the client's sticky turn state across reconnects, as today's core does.
		w.Header().Set("X-Codex-Turn-State", ts)
	}
	// A request that passed the client key check may come from any origin, as in today's core:
	// a web page cannot set the key's header on a WebSocket. Without client keys, a web page on
	// another site could drive core, so the library's same-origin check stays.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover,
		InsecureSkipVerify: len(cfg.ClientKeys) > 0})
	if err != nil {
		return // Accept has written the HTTP error
	}
	conn.SetReadLimit(MaxRequestBody)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s := &wsSocket{conn: conn, r: r, src: src, ctx: ctx, noSession: map[string]bool{}, codexClient: isCodexClient(r)}
	defer s.close()
	queue := make(chan wsMessage, wsQueue)
	go s.read(queue, cancel)
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-queue:
			if msg.epoch < s.epoch.Load() {
				// Written before the client heard of an upstream loss: the client resends.
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

// wsSocket is one client socket.
type wsSocket struct {
	conn        *websocket.Conn
	r           *http.Request // the upgrade request: its headers stand for every turn's
	src         Source
	ctx         context.Context
	codexClient bool

	// Owned by the loop that runs turns.
	session    Session
	sessionExt string
	noSession  map[string]bool // extension and account that take no sessions
	transcript openairesponses.Transcript
	// pinned is the account a session turn last ran on; later turns stay on it, as in today's
	// core ("the next full create reconnects on it, never on another one").
	pinned    string
	lastModel string
	lastMode  string // the mode of the last turn that completed: wsModeSession, wsModeHTTP or ""

	// epoch counts upstream losses; each queued message carries the count when it was read.
	epoch atomic.Uint64

	mu   sync.Mutex
	turn *wsTurn // the running turn, for interrupts

	// capture keeps the messages sent during the running turn for its request log; nil when
	// none is kept.
	capture atomic.Pointer[capture]
}

// wsMessage is a client message waiting for the turn loop.
type wsMessage struct {
	data  []byte
	epoch uint64
}

const (
	wsModeSession = "session"
	wsModeHTTP    = "http"
)

// wsTurn is the running turn as an interrupt sees it.
type wsTurn struct {
	mode        string
	session     Session
	responseID  string // from the turn's response.start
	cancel      context.CancelFunc
	interrupted string // the client's response_id, once an HTTP turn was interrupted
}

// read reads client messages: an interrupt is answered at once, anything else waits in queue
// for the turn loop. The socket ending ends the loop.
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

// send writes one text message.
func (s *wsSocket) send(data []byte) error {
	if c := s.capture.Load(); c != nil {
		_, _ = c.Write(data)
		_, _ = c.Write([]byte("\n"))
	}
	return s.conn.Write(s.ctx, websocket.MessageText, data)
}

// sendError writes an error message: {"type":"error","status":...,"error":{...}}. The error
// object is the upstream's when it sent one, else built from e as an HTTP error body is, with a
// code only when e has a provider code (core's own refusals carry none, as in today's core).
func (s *wsSocket) sendError(e *llm.Error) error {
	status, body := openaichat.ErrorResponse(e)
	var b struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	obj := map[string]json.RawMessage(nil)
	if json.Unmarshal(e.Raw, &b) == nil && b.Error != nil {
		obj = b.Error
	} else if json.Unmarshal(body, &b) == nil {
		obj = b.Error
		if e.ProviderCode == "" {
			delete(obj, "code")
		}
	}
	m := map[string]any{"type": "error", "status": status, "error": obj}
	if u := e.UsageLimit; u != nil {
		// The usage-limit reply's headers ride in the message, as today's core sends them.
		h := map[string]string{}
		for _, kv := range u.Headers() {
			h[kv[0]] = kv[1]
		}
		m["headers"] = h
	}
	msg, _ := json.Marshal(m)
	return s.send(msg)
}

// refuseTurn sends e as a turn's error, marks the turn failed for its request log (t, when one is
// kept), and returns false when the socket must close.
func (s *wsSocket) refuseTurn(t *trace, e *llm.Error) bool {
	if t != nil && e.Code != llm.CodeCancelled {
		t.failed = true
	}
	return s.sendError(e) == nil
}

func clientError(status int, msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: status, Scope: llm.ScopeRequest, Message: msg}
}

// interrupt answers a response.interrupt: on a session it goes to the extension when it targets
// the running response; on an HTTP turn core cancels the turn and ends it for the client.
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
		// An HTTP upstream cannot be told: cancel the turn; the turn loop ends it for the
		// client with response.incomplete for the client's response ID.
		if t.interrupted == "" {
			t.interrupted = id
			t.cancel()
		}
		s.mu.Unlock()
	}
}

// handle runs one queued client message. It returns false when the socket must close.
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

	// A turn that continues a response held on a session must go to that session.
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

// runTurn routes one turn and writes its reply. mode is the mode the turn must take, or "" to
// let the chosen account decide. It returns false when the socket must close.
func (s *wsSocket) runTurn(g *Generation, msg map[string]json.RawMessage, model, mode string) bool {
	arrived := time.Now()
	// The client's message with the model filled in: as it is, it is a session turn's request.
	native := make(map[string]json.RawMessage, len(msg))
	for k, v := range msg {
		native[k] = v
	}
	native["model"], _ = json.Marshal(model)
	nativeBody, _ := json.Marshal(native)
	// The turn's request log holds the client's message and what was sent back, as a request's
	// holds its body and reply.
	var turnTrace *trace
	if g.RequestLog != nil {
		turnLog := g.RequestLog.newCapture()
		turnTrace = newTrace()
		s.capture.Store(turnLog)
		defer func() {
			s.capture.Store(nil)
			g.RequestLog.write(&requestEntry{at: arrived, method: s.r.Method, url: s.r.URL, client: ClientIP(s.r), headers: s.r.Header, body: nativeBody,
				trace: turnTrace, reply: turnLog.bytes(), replyTitle: "WEBSOCKET MESSAGES"})
		}()
	}
	sessReq, err := openairesponses.ParseRequest(nativeBody)
	if err != nil {
		return s.refuseTurn(turnTrace, asLLMError(err))
	}
	sessReq.DropPolicy = g.Config.DropPolicy
	takeThinkingSuffix(&sessReq)
	s.nativeHeaders(&sessReq)

	// An HTTP turn's request carries the whole conversation.
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
		httpReq.DropPolicy = g.Config.DropPolicy
		takeThinkingSuffix(&httpReq)
		s.nativeHeaders(&httpReq)
	}

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	turn := &wsTurn{cancel: cancel}
	var opened []Session // sessions this turn opened
	var sess Session     // the session the last attempt ran on; nil for an HTTP attempt
	var sessExt string
	chosen := "" // the mode of the last attempt
	ranOn := ""  // the account of the last attempt
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
		// The response it continues is on the session: only the session's account can run it.
		if rr.Pin == "" || rr.Pin == s.pinned {
			rr.Pin = s.session.AccountID()
		}
		rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, _ llm.Request) route.Stream {
			if ext != s.sessionExt || a.ID != s.session.AccountID() || s.session.Ended() {
				attempted("", nil, "")
				return failedTurn(openairesponses.PreviousResponseNotFound())
			}
			attempted(wsModeSession, s.session, ext)
			return s.session.Turn(ctx, a, sessReq)
		}
	case wsModeHTTP:
		rr.LLM = httpReq
		rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, _ llm.Request) route.Stream {
			attempted(wsModeHTTP, nil, "")
			return g.Attempt(ctx, ext, a, httpReq)
		}
	default:
		rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, _ llm.Request) route.Stream {
			if s.session != nil && !s.session.Ended() && s.sessionExt == ext && s.session.AccountID() == a.ID {
				attempted(wsModeSession, s.session, ext)
				return s.session.Turn(ctx, a, sessReq)
			}
			key := ext + "\x00" + a.ID
			if g.OpenSession != nil && !s.noSession[key] {
				ns, e := g.OpenSession(ctx, ext, a, protocol.SessionInfo{Format: protocol.SessionOpenAIResponses})
				if e == nil {
					opened = append(opened, ns)
					attempted(wsModeSession, ns, ext)
					return ns.Turn(ctx, a, sessReq)
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
			return g.Attempt(ctx, ext, a, httpReq)
		}
	}
	s.mu.Lock()
	s.turn = turn
	s.mu.Unlock()
	// The turn is over for an interrupt once its last message is on its way to the client: a
	// client that has read that message and interrupts next must be told no turn is running.
	endTurn := func() {
		s.mu.Lock()
		s.turn = nil
		s.mu.Unlock()
	}
	defer endTurn()

	turnReq := sessReq
	turnReq.Stream = true
	m := g.newMeter(s.r, arrived, turnReq, clientToken(s.r, false))
	var attemptExt string // the extension of the last account tried
	rr.Attempting = func(a route.Attempt) {
		attemptExt = a.Extension
		m.attempt(a, "")
		if turnTrace != nil {
			turnTrace.account = a.Account
		}
	}
	stream := meterStream(g.Router.Generate(ctx, rr), m)
	defer stream.Close()
	ev := stream.Next()
	// The session the turn ran on is the socket's from now on, whatever the outcome; the
	// others it opened, and the socket's last one when another replaced it, are closed.
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
	var output []json.RawMessage // finished output items, for an interrupted HTTP turn
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
	// A Responses-lite turn on a Codex account reaches the client as the upstream sent it, with
	// an empty output left empty, as today's core does (its preserveNativeOutput).
	sw.KeepOutput = attemptExt == legacy.Codex && liteTurn(sessReq)
	defer func() { logTranslation(s.r, nil, sw) }()
	for {
		if ev.Type == llm.EventError {
			s.mu.Lock()
			id := turn.interrupted
			s.mu.Unlock()
			if id == "" {
				if chosen == wsModeSession && ev.Error.ProviderCode == openairesponses.CodeUpstreamWebsocketLost {
					// The next full create reconnects on the same account, and what the client
					// sent before hearing of the loss is refused.
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
			// An HTTP turn the client interrupted: it ends for the client, and the conversation
			// keeps what it said so far.
			if s.send(openairesponses.InterruptedMessage(id, output)) != nil {
				return false
			}
			completed = marshalItems(output)
			break
		}
		if err := sw.Write(ev); err != nil {
			return false
		}
		if m != nil {
			m.sent = http.StatusOK // the turn's reply has begun
		}
		if ev.Terminal() {
			if chosen == wsModeHTTP && httpTurn.Warmup {
				// A warmup reaches no upstream: nothing to record.
				m.drop()
			} else if ev.Type == llm.EventFinish {
				m.finish(ev)
			}
			break
		}
		ev = stream.Next()
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

// liteTurn reports whether the client's turn is a Responses-lite one.
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

// nativeHeaders gives a turn's request the client's Responses headers from the upgrade request.
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

// turnFailed handles a turn that ended in an error, as today's core does: a lost upstream socket
// or a continuation of a response no socket holds is reported and the socket stays open; an
// upstream close for a message too big closes the socket with 1009; a request the upstream
// found at fault is reported and the socket closes; any other failure closes the socket
// without a message. It returns false when the socket must close.
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

// exposed reports whether a failed turn's error reaches the client before the socket closes:
// the upstream refused the request itself (status 400, 409, 413 or 422, or an error of type
// invalid_request_error), and not for the account's sake (401, 402, 403, 429).
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

// cutReason cuts a close reason to wsCloseReasonMax bytes on a UTF-8 boundary.
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

// failedTurn is a reply that is one error.
func failedTurn(e *llm.Error) route.Stream {
	return &eventsStream{evs: []llm.Event{{Type: llm.EventError, Error: e}}}
}

// eventsStream is a reply made of the given events, the last of them terminal.
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

// newWarmup answers a warmup (generate: false) on an account whose upstream holds no socket
// state, as today's core does, without an upstream request: an empty response, created and
// completed. The events carry the Responses messages in raw, which the client gets as they are.
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
