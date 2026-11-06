// Package sdk implements the extension side of the pluxy extension protocol for Go extensions:
// the handshake, framing, cancellation and shutdown. An extension supplies a Generate handler,
// and Login and Refresh handlers if it signs in to accounts, and calls Serve from main. See
// ../protocol/TUTORIAL.md.
package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Call is one generate call.
type Call struct {
	ID      string
	Account protocol.Account
	Request llm.Request
}

// Handler answers a generate call by writing events to out. It ends with out.Finish or by
// returning an error: an *llm.Error is sent as it is, any other error as code internal.
// ctx is cancelled when core cancels the call.
type Handler func(ctx context.Context, call Call, out *Stream) error

// LoginCall is one login call: sign in to a new account, or again to an existing one.
type LoginCall struct {
	ID string
	// Account is the account being signed in to: its ID and settings, never a credential.
	Account protocol.Account
	// Method is one of Extension.LoginMethods, by its Method.
	Method string
	// Input is what the user entered when starting the login.
	Input map[string]string
}

// LoginHandler signs in to an account and returns its credential. It may prompt the user and
// wait for their input through s. ctx is cancelled when core cancels the login. An *llm.Error
// is sent as it is (code auth when the user or provider refused), any other error as code
// internal.
type LoginHandler func(ctx context.Context, call LoginCall, s *LoginSession) (*protocol.Grant, error)

// RefreshCall is one refresh call.
type RefreshCall struct {
	ID string
	// Account carries the credential to renew.
	Account protocol.Account
}

// RefreshHandler renews a credential and returns the new one, whole: core replaces the stored
// credential with it. A refusal of the grant itself (for example OAuth invalid_grant) is code
// auth with scope credential and not retryable: the account then needs a new login. A
// failure that may pass (the token endpoint is down or rate limited) is retryable.
type RefreshHandler func(ctx context.Context, call RefreshCall) (*protocol.Credential, error)

// Extension describes the extension to Serve.
type Extension struct {
	ID       string
	Version  string
	Generate Handler
	// LoginMethods are the ways Login can sign in; required with Login.
	LoginMethods []protocol.LoginMethod
	Login        LoginHandler
	Refresh      RefreshHandler
}

// operations lists the operations ext serves.
func (ext Extension) operations() []string {
	ops := []string{protocol.OpGenerate}
	if ext.Login != nil {
		ops = append(ops, protocol.OpLogin)
	}
	if ext.Refresh != nil {
		ops = append(ops, protocol.OpRefresh)
	}
	return ops
}

// Serve runs the extension until core closes its stdin. It returns an error when the program was
// not started by pluxy, the protocol versions do not match, or the socket cannot be served.
func Serve(ext Extension) error {
	return serve(ext, os.Stdin, os.Stdout)
}

func serve(ext Extension, stdin io.Reader, stdout io.Writer) error {
	offered := os.Getenv(protocol.EnvProtocolVersions)
	if offered == "" {
		return errors.New("this program is a pluxy extension; pluxy starts it")
	}
	versions, err := protocol.ParseVersions(offered)
	if err != nil {
		return err
	}
	if !slices.Contains(versions, protocol.Version) {
		return fmt.Errorf("core offers protocol versions %v; this extension speaks %d", versions, protocol.Version)
	}
	socket := os.Getenv(protocol.EnvSocket)
	if socket == "" {
		return fmt.Errorf("%s is not set", protocol.EnvSocket)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	s := &server{ext: ext, conns: map[net.Conn]*callState{}}
	if _, err := io.WriteString(stdout, protocol.HandshakeLine(protocol.Version, socket)); err != nil {
		ln.Close()
		return err
	}
	// Core closes stdin to stop us, and stdin also closes if core dies.
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			break
		}
		if !s.track(conn) {
			conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
	s.shutdown()
	s.wg.Wait()
	return nil
}

// shutdownGrace is how long a cancelled call has to send its last frame at shutdown before its
// connection is cut, so a handler blocked writing to core still returns.
const shutdownGrace = time.Second

type server struct {
	ext   Extension
	wg    sync.WaitGroup
	mu    sync.Mutex
	conns map[net.Conn]*callState // every open connection; nil until it runs a generate call
	done  bool
}

type callState struct{ cancel context.CancelFunc }

// track records an accepted connection, unless shutdown has begun.
func (s *server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false
	}
	s.conns[conn] = nil
	return true
}

// shutdown cancels running calls, gives them shutdownGrace to send their last frame, and closes
// every other connection at once, including ones still waiting for their first frame.
func (s *server) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	for conn, st := range s.conns {
		if st == nil {
			conn.Close()
			continue
		}
		st.cancel()
		_ = conn.SetDeadline(time.Now().Add(shutdownGrace))
	}
}

func (s *server) handle(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	var call protocol.Call
	if err := protocol.ReadFrame(conn, &call); err != nil {
		return
	}
	switch call.Op {
	case protocol.OpDescribe:
		_ = protocol.WriteFrame(conn, protocol.Reply{
			Event: llm.Event{Type: protocol.TypeDescribeResult},
			Info: &protocol.Info{
				ID: s.ext.ID, Version: s.ext.Version, Protocol: protocol.Version,
				Operations: s.ext.operations(), LoginMethods: s.ext.LoginMethods,
			},
		})
	case protocol.OpGenerate:
		s.generate(conn, call)
	case protocol.OpLogin:
		s.login(conn, call)
	case protocol.OpRefresh:
		s.refresh(conn, call)
	default:
		_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{
			Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown operation %q", call.Op), Scope: llm.ScopeRequest,
		}}})
	}
}

func (s *server) generate(conn net.Conn, call protocol.Call) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()

	// Core sends nothing after the call frame; the read ends when core closes the connection,
	// which is how it cancels.
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()

	out := &Stream{w: conn}
	c := Call{ID: call.ID}
	if call.Account != nil {
		c.Account = *call.Account
	}
	if call.Request != nil {
		c.Request = *call.Request
	}
	var err error
	if s.ext.Generate == nil {
		err = &llm.Error{Code: llm.CodeUnsupported, Message: "this extension does not generate", Scope: llm.ScopeRequest}
	} else {
		err = runHandler(ctx, s.ext.Generate, c, out)
	}
	if out.broken || out.seq.Done() {
		return
	}
	if err == nil {
		err = &llm.Error{Code: llm.CodeInternal, Message: "the extension returned without finishing the reply"}
	}
	_ = out.send(llm.Event{Type: llm.EventError, Error: asError(ctx, err)})
}

// asError turns a handler's error into the error event's payload.
func asError(ctx context.Context, err error) *llm.Error {
	var le *llm.Error
	if errors.As(err, &le) {
		return le
	}
	code := llm.CodeInternal
	if ctx.Err() != nil {
		code = llm.CodeCancelled
	}
	return &llm.Error{Code: code, Message: err.Error()}
}

// begin registers a running call so shutdown can cancel it, and returns its context; ok is
// false when shutdown has begun.
func (s *server) begin(conn net.Conn) (ctx context.Context, cancel context.CancelFunc, ok bool) {
	ctx, cancel = context.WithCancel(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		cancel()
		return nil, nil, false
	}
	s.conns[conn] = &callState{cancel: cancel}
	return ctx, cancel, true
}

func (s *server) login(conn net.Conn, call protocol.Call) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()
	sess := &LoginSession{inputs: make(chan map[string]string, 1), w: conn}
	// After the call frame core sends only login.input frames; the read ends when core closes
	// the connection, which is how it cancels.
	go func() {
		defer cancel()
		for {
			var f protocol.Call
			if err := protocol.ReadFrame(conn, &f); err != nil {
				return
			}
			if f.Type != protocol.TypeLoginInput {
				continue
			}
			select {
			case sess.inputs <- f.Input:
			case <-ctx.Done():
				return
			}
		}
	}()
	c := LoginCall{ID: call.ID}
	if call.Account != nil {
		c.Account = *call.Account
		c.Account.Credential = nil
	}
	if call.Login != nil {
		c.Method, c.Input = call.Login.Method, call.Login.Input
	}
	var grant *protocol.Grant
	var err error
	switch {
	case s.ext.Login == nil:
		err = &llm.Error{Code: llm.CodeUnsupported, Message: "this extension does not log in", Scope: llm.ScopeRequest}
	case !slices.ContainsFunc(s.ext.LoginMethods, func(m protocol.LoginMethod) bool { return m.Method == c.Method }):
		err = &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown login method %q", c.Method), Scope: llm.ScopeRequest}
	default:
		grant, err = runLogin(ctx, s.ext.Login, c, sess)
	}
	if err == nil && (grant == nil || grant.Credential == nil || grant.Credential.Kind == "") {
		err = &llm.Error{Code: llm.CodeInternal, Message: "the extension's login returned no credential"}
	}
	if err != nil {
		_ = sess.write(protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: asError(ctx, err)}})
		return
	}
	_ = sess.write(protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginDone}, Grant: grant})
}

func runLogin(ctx context.Context, h LoginHandler, c LoginCall, s *LoginSession) (g *protocol.Grant, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	return h(ctx, c, s)
}

func (s *server) refresh(conn net.Conn, call protocol.Call) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	c := RefreshCall{ID: call.ID}
	if call.Account != nil {
		c.Account = *call.Account
	}
	var cred *protocol.Credential
	var err error
	if s.ext.Refresh == nil {
		err = &llm.Error{Code: llm.CodeUnsupported, Message: "this extension does not refresh credentials", Scope: llm.ScopeRequest}
	} else {
		cred, err = runRefresh(ctx, s.ext.Refresh, c)
	}
	if err == nil && (cred == nil || cred.Kind == "") {
		err = &llm.Error{Code: llm.CodeInternal, Message: "the extension's refresh returned no credential"}
	}
	reply := protocol.Reply{Event: llm.Event{Type: protocol.TypeRefreshDone}, Grant: &protocol.Grant{Credential: cred}}
	if err != nil {
		reply = protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: asError(ctx, err)}}
	}
	_ = protocol.WriteFrame(conn, reply)
}

func runRefresh(ctx context.Context, h RefreshHandler, c RefreshCall) (cred *protocol.Credential, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	return h(ctx, c)
}

// LoginSession is a running login's line to the user, through core.
type LoginSession struct {
	inputs chan map[string]string
	mu     sync.Mutex
	w      io.Writer
	broken bool
}

// Prompt tells the user what to do next. A later prompt replaces an earlier one.
func (s *LoginSession) Prompt(p protocol.Prompt) error {
	return s.write(protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginPrompt}, Prompt: &p})
}

// Input waits for what the user enters in answer to a prompt that has Inputs. It returns
// ctx's error when ctx ends first.
func (s *LoginSession) Input(ctx context.Context) (map[string]string, error) {
	select {
	case in := <-s.inputs:
		return in, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *LoginSession) write(r protocol.Reply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return errors.New("sdk: connection to core is closed")
	}
	if err := protocol.WriteFrame(s.w, r); err != nil {
		s.broken = true
		return err
	}
	return nil
}

func runHandler(ctx context.Context, h Handler, c Call, out *Stream) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	return h(ctx, c, out)
}

// Stream writes the events of one reply. Its methods return an error when the event would break
// the protocol's ordering rules or the connection to core is gone; the handler should then
// return.
type Stream struct {
	w      io.Writer
	seq    llm.Sequence
	broken bool
}

// Send writes one event after checking it against the ordering rules.
func (s *Stream) Send(ev llm.Event) error {
	if err := s.seq.Check(ev); err != nil {
		return fmt.Errorf("sdk: %w", err)
	}
	return s.write(ev)
}

func (s *Stream) send(ev llm.Event) error {
	if err := s.seq.Check(ev); err != nil {
		return err
	}
	return s.write(ev)
}

func (s *Stream) write(ev llm.Event) error {
	if s.broken {
		return errors.New("sdk: connection to core is closed")
	}
	if err := protocol.WriteFrame(s.w, protocol.Reply{Event: ev}); err != nil {
		s.broken = true
		return err
	}
	return nil
}

// Start sends response.start.
func (s *Stream) Start(info llm.ResponseInfo) error {
	return s.Send(llm.Event{Type: llm.EventResponseStart, Response: &info})
}

// StartItem opens the next output item and returns its index.
func (s *Stream) StartItem(item llm.ItemStart) (int, error) {
	i := s.seq.NextIndex()
	return i, s.Send(llm.Event{Type: llm.EventItemStart, Index: i, Item: &item})
}

// Text sends text for an open message item.
func (s *Stream) Text(index int, delta string) error {
	return s.Send(llm.Event{Type: llm.EventTextDelta, Index: index, Delta: delta})
}

// Reasoning sends reasoning text for an open reasoning item.
func (s *Stream) Reasoning(index int, delta string) error {
	return s.Send(llm.Event{Type: llm.EventReasoningDelta, Index: index, Delta: delta})
}

// ToolInput sends a fragment of a tool call's arguments.
func (s *Stream) ToolInput(index int, delta string) error {
	return s.Send(llm.Event{Type: llm.EventToolInputDelta, Index: index, Delta: delta})
}

// EndItem closes an output item.
func (s *Stream) EndItem(index int) error {
	return s.Send(llm.Event{Type: llm.EventItemEnd, Index: index})
}

// Finish ends the reply successfully.
func (s *Stream) Finish(f llm.Finish, usage *llm.Usage) error {
	return s.Send(llm.Event{Type: llm.EventFinish, Finish: &f, Usage: usage})
}
