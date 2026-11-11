package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sync"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// HoldsSessions reports whether an extension that described itself with info holds sessions: it
// implements minor version 1 or later and lists the session operation.
func HoldsSessions(info protocol.Info) bool {
	return info.ProtocolMinor >= 1 && slices.Contains(info.Operations, protocol.OpSession)
}

// Session is an open session on an extension: one connection that carries turns, one at a time,
// on one account (see Sessions in the protocol README). Closing it ends the session.
type Session struct {
	ext       *Extension
	conn      net.Conn
	accountID string

	frames  chan sessionFrame // the reader's next frame; unbuffered, so a slow turn slows the extension
	done    chan struct{}     // closed when the reader has stopped: the connection has ended
	closing chan struct{}     // closed by Close

	writeMu sync.Mutex // turn and interrupt frames

	mu         sync.Mutex
	creds      []*protocol.Credential // every credential and settings' secrets the session has carried, for redaction
	running    bool                   // a turn has been sent and its terminal event not read
	closeAfter bool                   // close once the running turn ends
	closed     bool
	onClose    []func()
}

type sessionFrame struct {
	reply protocol.Reply
}

// OpenSession opens a session of the given kind on account. ctx bounds only the opening; the
// session lasts until it is closed. An extension that holds no sessions, or refuses this one,
// returns an error event's payload: code unsupported with scope credential means the account
// takes no sessions.
func (e *Extension) OpenSession(ctx context.Context, account protocol.Account, info protocol.SessionInfo) (*Session, *llm.Error) {
	if !HoldsSessions(e.info) {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Message: fmt.Sprintf("extension %s holds no sessions", e.manifest.ID)}
	}
	select {
	case <-e.exited:
		return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s is not running", e.manifest.ID)}
	default:
	}
	proxy, perr := e.proxied(&account)
	if perr != nil {
		return nil, perr
	}
	creds := append(e.holdAccount(account), proxy)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", e.socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
		}
		return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s is not reachable: %v", e.manifest.ID, err)}
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpSession, Account: &account, Session: &info}
	if err := protocol.WriteFrame(conn, call); err != nil {
		conn.Close()
		return nil, e.sessionReadError(ctx, protocol.NewRedactor(creds...), err)
	}
	var reply protocol.Reply
	if err := protocol.ReadFrame(conn, &reply); err != nil {
		conn.Close()
		return nil, e.sessionReadError(ctx, protocol.NewRedactor(creds...), err)
	}
	switch reply.Type {
	case protocol.TypeSessionReady:
	case llm.EventError:
		conn.Close()
		if reply.Error == nil {
			return nil, e.protocolError("an error frame without an error")
		}
		ev := protocol.NewRedactor(creds...).Event(reply.Event)
		return nil, ev.Error
	default:
		conn.Close()
		return nil, redacted(protocol.NewRedactor(creds...), e.protocolError(fmt.Sprintf("a session call was answered with %q", reply.Type)))
	}
	if !stop() {
		// ctx ended just as the session opened; its conn is closed.
		return nil, &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
	}
	s := &Session{ext: e, conn: conn, accountID: account.ID, frames: make(chan sessionFrame), done: make(chan struct{}), closing: make(chan struct{})}
	for _, c := range creds {
		if c != nil {
			s.creds = append(s.creds, c)
		}
	}
	go s.read()
	return s, nil
}

func (e *Extension) protocolError(what string) *llm.Error {
	return &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s broke the protocol: %s", e.manifest.ID, what)}
}

// redacted removes credentials and settings secrets from an error core builds from what the
// extension sent, such as a protocol error that quotes a frame's type.
func redacted(r *protocol.Redactor, e *llm.Error) *llm.Error {
	e.Message = r.String(e.Message)
	return e
}

func (e *Extension) sessionReadError(ctx context.Context, redact *protocol.Redactor, err error) *llm.Error {
	s := &Stream{ctx: ctx, ext: e, redact: redact}
	return s.readError(err)
}

// read hands every frame of the connection to the running turn, one at a time. When the
// connection ends, the session is over.
func (s *Session) read() {
	defer close(s.done)
	defer s.Close()
	for {
		var f sessionFrame
		if err := protocol.ReadFrame(s.conn, &f.reply); err != nil {
			return
		}
		select {
		case s.frames <- f:
		case <-s.closing:
			return
		}
	}
}

// AccountID returns the account the session belongs to.
func (s *Session) AccountID() string { return s.accountID }

// Done is closed when the session has ended: core closed it, or the extension closed the
// connection or stopped.
func (s *Session) Done() <-chan struct{} { return s.done }

// Ended reports whether the session has ended or is being closed.
func (s *Session) Ended() bool {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return true
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Turn sends one turn and returns its reply. The account must be the session's; its credential
// may be newer than the one the session opened with. Cancelling ctx, or closing the stream
// before its terminal event, cancels the turn, which ends the session.
func (s *Session) Turn(ctx context.Context, account protocol.Account, req llm.Request) *SessionStream {
	t := &SessionStream{s: s, ctx: ctx}
	// A turn refused here was never sent: closing its stream leaves the session alone.
	refuse := func(e *llm.Error) *SessionStream {
		t.ended = true
		t.fail(e)
		return t
	}
	if account.ID != s.accountID {
		return refuse(&llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("session of account %q got a turn for account %q", s.accountID, account.ID)})
	}
	proxy, perr := s.ext.proxied(&account)
	if perr != nil {
		return refuse(perr)
	}
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return refuse(&llm.Error{Code: llm.CodeExtensionUnavailable, Message: "the session has ended"})
	case s.running:
		s.mu.Unlock()
		return refuse(&llm.Error{Code: llm.CodeInternal, Message: "a turn is already running on this session"})
	}
	for _, c := range s.ext.holdAccount(account) {
		if c != nil && !slices.ContainsFunc(s.creds, func(o *protocol.Credential) bool { return sameCredential(o, c) }) {
			s.creds = append(s.creds, c)
		}
	}
	if proxy != nil && !slices.ContainsFunc(s.creds, func(o *protocol.Credential) bool { return sameCredential(o, proxy) }) {
		s.creds = append(s.creds, proxy)
	}
	s.running = true
	t.redact = protocol.NewRedactor(s.creds...)
	s.mu.Unlock()

	// A frame the extension sent between turns breaks the protocol; the connection ending
	// between turns ends the session.
	select {
	case f := <-s.frames:
		s.Close()
		t.end()
		t.fail(redacted(t.redact, s.ext.protocolError(fmt.Sprintf("a %q frame between turns", f.reply.Type))))
		return t
	default:
	}
	t.stop = context.AfterFunc(ctx, s.Close)
	s.writeMu.Lock()
	err := protocol.WriteFrame(s.conn, protocol.Call{Type: protocol.TypeSessionTurn, Account: &account, Request: &req})
	s.writeMu.Unlock()
	if err != nil {
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			// Nothing was written: the session can take the next turn.
			t.end()
			t.fail(&llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusRequestEntityTooLarge, Scope: llm.ScopeRequest,
				Message: "request is too large to pass to the provider extension"})
			return t
		}
		s.Close()
		t.fail((&Stream{ctx: ctx, ext: s.ext, redact: t.redact}).readError(err))
	}
	return t
}

// Interrupt passes the client's control message for the running turn to the extension. It
// does nothing when no turn runs.
func (s *Session) Interrupt(raw []byte) {
	s.mu.Lock()
	running := s.running && !s.closed
	s.mu.Unlock()
	if !running {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := protocol.WriteFrame(s.conn, protocol.Call{Type: protocol.TypeSessionInterrupt, Raw: raw}); err != nil {
		go s.Close()
	}
}

// CloseWhenIdle closes the session now if no turn runs, else once the running turn ends.
func (s *Session) CloseWhenIdle() {
	s.mu.Lock()
	if s.running {
		s.closeAfter = true
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.Close()
}

// OnClose runs f once the session is closed (at once if it is already closed).
func (s *Session) OnClose(f func()) {
	s.mu.Lock()
	if !s.closed {
		s.onClose = append(s.onClose, f)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	f()
}

// Close ends the session: the extension sees its connection close, cancels a running turn and
// closes its upstream connection.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.closing)
	fs := s.onClose
	s.onClose = nil
	s.mu.Unlock()
	s.conn.Close()
	for _, f := range fs {
		f()
	}
}

// SessionStream is the reply to one turn. Next returns events until and including exactly one
// terminal event, then keeps returning it.
type SessionStream struct {
	s      *Session
	ctx    context.Context
	redact *protocol.Redactor
	seq    llm.Sequence
	stop   func() bool
	final  *llm.Event
	ended  bool
}

func (t *SessionStream) fail(e *llm.Error) {
	ev := llm.Event{Type: llm.EventError, Error: e}
	t.final = &ev
}

// end marks the turn over on the session, which can then take the next one.
func (t *SessionStream) end() {
	if t.ended {
		return
	}
	t.ended = true
	if t.stop != nil {
		t.stop()
	}
	s := t.s
	s.mu.Lock()
	s.running = false
	closeNow := s.closeAfter
	s.mu.Unlock()
	if closeNow {
		s.Close()
	}
}

// Next returns the next event of the turn.
func (t *SessionStream) Next() llm.Event {
	if t.final != nil {
		return *t.final
	}
	s := t.s
	var f sessionFrame
	select {
	case f = <-s.frames:
	case <-s.done:
		// The connection ended before the turn's terminal event: core cancelled the turn, or
		// the extension closed the connection or stopped.
		t.end()
		t.fail((&Stream{ctx: t.ctx, ext: s.ext}).readError(net.ErrClosed))
		return *t.final
	}
	if err := t.seq.Check(f.reply.Event); err != nil {
		s.Close()
		t.end()
		t.fail(redacted(t.redact, s.ext.protocolError(err.Error())))
		return *t.final
	}
	ev := t.redact.Event(f.reply.Event)
	if ev.Terminal() {
		t.final = &ev
		t.end()
	}
	return ev
}

// Close ends the turn; if it had not finished, that cancels it and ends the session.
func (t *SessionStream) Close() {
	if !t.ended {
		t.s.Close()
		t.end()
	}
}

func sameCredential(a, b *protocol.Credential) bool {
	return a.Kind == b.Kind && a.APIKey == b.APIKey && a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken &&
		a.ExpiresAt == b.ExpiresAt && a.RefreshAt == b.RefreshAt && string(a.Data) == string(b.Data)
}
