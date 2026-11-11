// Package sdk implements the extension side of the pluxy extension protocol for Go extensions:
// the handshake, framing, cancellation and shutdown. An extension supplies a Generate handler,
// Login and Refresh handlers if it signs in to accounts, a Session handler if it holds
// sessions, a Models handler if it lists its accounts' models, a Quota handler if it reads their
// quota, and calls Serve from main. See
// ../protocol/TUTORIAL.md.
package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

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
// internal. An error is retryable only when the request never reached the provider: use
// TrackSend to tell. A 5xx answer, and a request that failed after it was sent, are not.
type LoginHandler func(ctx context.Context, call LoginCall, s *LoginSession) (*protocol.Grant, error)

// RefreshCall is one refresh call.
type RefreshCall struct {
	ID string
	// Account carries the credential to renew.
	Account protocol.Account
}

// RefreshHandler renews a credential and returns the new one, whole: core replaces the stored
// credential with it. A refusal of the grant itself (for example OAuth invalid_grant) is code
// auth with scope credential and not retryable: the account then needs a new login. Any other
// failure is retryable only when the request never reached the token endpoint (use TrackSend to
// tell), or when the endpoint rate limited it (429). A 5xx answer, and a request that failed after
// it was sent, are not retryable: the endpoint may have run the request.
type RefreshHandler func(ctx context.Context, call RefreshCall) (*protocol.Credential, error)

// ModelsCall is one models call.
type ModelsCall struct {
	ID string
	// Account is the account whose models to list, with its credential.
	Account protocol.Account
}

// ModelsHandler lists the models an account serves, by their upstream names, and who provides
// them (protocol minor version 3). Return an empty list for an account the extension has no
// list for. An *llm.Error is sent as it is, any other error as code internal.
type ModelsHandler func(ctx context.Context, call ModelsCall) (*protocol.ModelList, error)

// CheckCall is one check call.
type CheckCall struct {
	ID string
	// Account is the account to check, with its credential.
	Account protocol.Account
}

// CheckHandler checks whether an account works without generating anything (protocol minor
// version 4): at most one upstream call, and only one that costs the account nothing, such as its
// model list or the provider's token check. Return the result for an account that works and an
// *llm.Error for one that does not: code auth for a refused key, unavailable for an upstream that
// cannot be reached, upstream for an upstream failure, and unsupported for an account that cannot
// be checked without a generation. Any other error is sent as code internal.
type CheckHandler func(ctx context.Context, call CheckCall) (*protocol.CheckResult, error)

// QuotaCall is one quota call.
type QuotaCall struct {
	ID string
	// Account is the account whose quota to read, with its credential.
	Account protocol.Account
}

// QuotaHandler reads what an account's plan allows and how much of it is used, from the
// provider's own usage endpoint, as the provider's own client does (protocol minor version 5).
// An account that has no quota to report (for example an API key) is code unsupported with scope
// credential. An *llm.Error is sent as it is, any other error as code internal.
type QuotaHandler func(ctx context.Context, call QuotaCall) (*protocol.Quota, error)

// SessionCall is one session call: open a session of a kind on an account.
type SessionCall struct {
	ID      string
	Account protocol.Account
	// Format is the kind of session (protocol.SessionOpenAIResponses).
	Format string
}

// SessionHandler opens a session and returns what answers its turns. It may connect to the
// upstream now or at the first turn. ctx lasts as long as the session. Returning an error
// refuses the session: an *llm.Error is sent as it is (code unsupported with scope credential
// for an account that takes no sessions: core then sends its requests as generate calls), any
// other error as code internal.
type SessionHandler func(ctx context.Context, call SessionCall) (Session, error)

// Session answers the turns of one session, one at a time.
type Session interface {
	// Turn answers one turn as a Handler answers a generate call. call.Account is the
	// session's account, with the newest credential core holds. ctx is cancelled when core
	// cancels the turn, which also ends the session.
	Turn(ctx context.Context, call Call, out *Stream) error
	// Interrupt passes the client's control message for the running turn upstream. It is
	// called from another goroutine while Turn runs, never otherwise.
	Interrupt(raw json.RawMessage)
	// Close ends the session, once, after the last turn has returned: close the upstream
	// connection here.
	Close()
}

// Extension describes the extension to Serve.
type Extension struct {
	ID       string
	Version  string
	Generate Handler
	// LoginMethods are the ways Login can sign in; required with Login.
	LoginMethods []protocol.LoginMethod
	Login        LoginHandler
	Refresh      RefreshHandler
	// Session, when set, holds sessions (protocol minor version 1).
	Session SessionHandler
	// CountTokens, when set, counts a request's input tokens (protocol minor version 2). It
	// answers as Generate does, with out.Start and then out.Finish whose usage holds
	// InputTokens (the count) and Raw (the upstream's count reply); an account that cannot
	// count returns code unsupported with scope credential, retryable.
	CountTokens Handler
	// Models, when set, lists an account's models (protocol minor version 3).
	Models ModelsHandler
	// Check, when set, checks an account without generating anything (protocol minor version
	// 4). Core calls it when it starts, and when a reload or a login adds or changes an account.
	Check CheckHandler
	// Quota, when set, reads an account's quota (protocol minor version 5). Login, when set,
	// should also report each grant's Identity (minor version 5), so core can check a new login
	// to an existing account.
	Quota QuotaHandler
	// Endpoint, when set, serves endpoint calls (protocol minor version 7): client requests to a
	// provider endpoint beyond generation, named by call.Request.Native.Format (such as
	// llm.FormatOpenAIResponsesCompact). It answers with out.StartBody, out.Body for each piece
	// of the reply's body, and out.Finish; an account or endpoint it does not serve returns code
	// unsupported with scope credential, retryable, so core tries another account.
	Endpoint Handler
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
	if ext.Session != nil {
		ops = append(ops, protocol.OpSession)
	}
	if ext.CountTokens != nil {
		ops = append(ops, protocol.OpCountTokens)
	}
	if ext.Quota != nil {
		ops = append(ops, protocol.OpQuota)
	}
	if ext.Models != nil {
		ops = append(ops, protocol.OpModels)
	}
	if ext.Check != nil {
		ops = append(ops, protocol.OpCheck)
	}
	if ext.Endpoint != nil {
		ops = append(ops, protocol.OpEndpoint)
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
	if call.Op != protocol.OpDescribe {
		if err := refuseProxy(call); err != nil {
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: err}})
			return
		}
	}
	switch call.Op {
	case protocol.OpDescribe:
		_ = protocol.WriteFrame(conn, protocol.Reply{
			Event: llm.Event{Type: protocol.TypeDescribeResult},
			Info: &protocol.Info{
				ID: s.ext.ID, Version: s.ext.Version, Protocol: protocol.Version, ProtocolMinor: protocol.Minor,
				Operations: s.ext.operations(), LoginMethods: s.ext.LoginMethods,
			},
		})
	case protocol.OpGenerate:
		s.generate(conn, call, s.ext.Generate, "this extension does not generate")
	case protocol.OpCountTokens:
		s.generate(conn, call, s.ext.CountTokens, "this extension does not count tokens")
	case protocol.OpEndpoint:
		s.generate(conn, call, s.ext.Endpoint, "this extension serves no endpoints")
	case protocol.OpLogin:
		s.login(conn, call)
	case protocol.OpRefresh:
		s.refresh(conn, call)
	case protocol.OpSession:
		s.session(conn, call)
	case protocol.OpModels:
		s.models(conn, call)
	case protocol.OpCheck:
		s.check(conn, call)
	case protocol.OpQuota:
		s.quota(conn, call)
	default:
		_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{
			Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown operation %q", call.Op), Scope: llm.ScopeRequest,
		}}})
	}
}

// generate answers a generate, count_tokens or endpoint call with handler h; none is the error message
// when the extension has no handler for it.
func (s *server) generate(conn net.Conn, call protocol.Call, h Handler, none string) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()
	ctx = withUpstreamInfo(ctx, call)

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
	if h == nil {
		err = &llm.Error{Code: llm.CodeUnsupported, Message: none, Scope: llm.ScopeRequest}
	} else {
		err = runHandler(ctx, h, c, out)
	}
	if out.broken || out.seq.Done() {
		return
	}
	if err == nil {
		err = &llm.Error{Code: llm.CodeInternal, Message: "the extension returned without finishing the reply"}
	}
	_ = out.send(llm.Event{Type: llm.EventError, Error: asError(ctx, err)})
}

// session holds one session: it answers session.ready, then each turn frame as a generate call
// on the session, and passes interrupts on while a turn runs. The connection closing ends the
// session.
func (s *server) session(conn net.Conn, call protocol.Call) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()
	ctx = withUpstreamInfo(ctx, call)
	refuse := func(err error) {
		_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: asError(ctx, err)}})
	}
	if s.ext.Session == nil {
		refuse(&llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeRequest, Message: "this extension holds no sessions"})
		return
	}
	// The reader runs from the start, so core closing the connection cancels the opening too.
	// It hands turns to the loop below and interrupts to the interrupt loop, so a slow
	// Interrupt does not stop it from seeing the connection close. Anything else, or the
	// connection closing, ends the session.
	turns := make(chan protocol.Call)
	interrupts := make(chan json.RawMessage, 16)
	go func() {
		defer cancel()
		for {
			var f protocol.Call
			if err := protocol.ReadFrame(conn, &f); err != nil {
				return
			}
			switch f.Type {
			case protocol.TypeSessionTurn:
				select {
				case turns <- f:
				case <-ctx.Done():
					return
				}
			case protocol.TypeSessionInterrupt:
				select {
				case interrupts <- f.Raw:
				default: // a client sending interrupts faster than they are passed on loses some
				}
			default:
				return
			}
		}
	}()

	sc := SessionCall{ID: call.ID}
	if call.Account != nil {
		sc.Account = *call.Account
	}
	if call.Session != nil {
		sc.Format = call.Session.Format
	}
	sess, err := openSession(ctx, s.ext.Session, sc)
	if err != nil {
		refuse(err)
		return
	}
	defer sess.Close()
	if err := protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeSessionReady}}); err != nil {
		return
	}

	var mu sync.Mutex
	running := false
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case raw := <-interrupts:
				mu.Lock()
				r := running
				mu.Unlock()
				if r {
					sess.Interrupt(raw)
				}
			}
		}
	}()
	for {
		var f protocol.Call
		select {
		case <-ctx.Done():
			return
		case f = <-turns:
		}
		c := Call{ID: call.ID}
		if f.Account != nil {
			c.Account = *f.Account
		}
		if f.Request != nil {
			c.Request = *f.Request
		}
		out := &Stream{w: conn}
		if err := refuseProxy(f); err != nil {
			if out.send(llm.Event{Type: llm.EventError, Error: err}) != nil {
				return
			}
			continue
		}
		mu.Lock()
		running = true
		mu.Unlock()
		err := runTurn(withUpstreamInfo(ctx, f), sess, c, out)
		mu.Lock()
		running = false
		mu.Unlock()
		if out.broken {
			return
		}
		if !out.seq.Done() {
			if err == nil {
				err = &llm.Error{Code: llm.CodeInternal, Message: "the extension returned without finishing the turn"}
			}
			if out.send(llm.Event{Type: llm.EventError, Error: asError(ctx, err)}) != nil {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func openSession(ctx context.Context, h SessionHandler, c SessionCall) (sess Session, err error) {
	defer func() {
		if r := recover(); r != nil {
			sess, err = nil, &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	sess, err = h(ctx, c)
	if err == nil && sess == nil {
		err = &llm.Error{Code: llm.CodeInternal, Message: "the session handler returned no session"}
	}
	return sess, err
}

func runTurn(ctx context.Context, sess Session, c Call, out *Stream) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	return sess.Turn(ctx, c, out)
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
	ctx = withUpstreamInfo(ctx, call)
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
	ctx = withUpstreamInfo(ctx, call)
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

func (s *server) models(conn net.Conn, call protocol.Call) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()
	ctx = withUpstreamInfo(ctx, call)
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	c := ModelsCall{ID: call.ID}
	if call.Account != nil {
		c.Account = *call.Account
	}
	var list *protocol.ModelList
	var err error
	if s.ext.Models == nil {
		err = &llm.Error{Code: llm.CodeUnsupported, Message: "this extension does not list models", Scope: llm.ScopeRequest}
	} else {
		list, err = runModels(ctx, s.ext.Models, c)
	}
	if err == nil && list == nil {
		list = &protocol.ModelList{}
	}
	if err == nil && list.Models == nil {
		list.Models = []protocol.Model{}
	}
	reply := protocol.Reply{Event: llm.Event{Type: protocol.TypeModelsResult}, Models: list}
	if err != nil {
		reply = protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: asError(ctx, err)}}
	}
	_ = protocol.WriteFrame(conn, reply)
}

// check answers a check call: one check.result or error frame.
func (s *server) check(conn net.Conn, call protocol.Call) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()
	ctx = withUpstreamInfo(ctx, call)
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	c := CheckCall{ID: call.ID}
	if call.Account != nil {
		c.Account = *call.Account
	}
	var res *protocol.CheckResult
	var err error
	if s.ext.Check == nil {
		err = &llm.Error{Code: llm.CodeUnsupported, Message: "this extension does not check accounts", Scope: llm.ScopeRequest}
	} else {
		res, err = runCheck(ctx, s.ext.Check, c)
	}
	if err == nil && res == nil {
		res = &protocol.CheckResult{}
	}
	reply := protocol.Reply{Event: llm.Event{Type: protocol.TypeCheckResult}, Check: res}
	if err != nil {
		reply = protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: asError(ctx, err)}}
	}
	_ = protocol.WriteFrame(conn, reply)
}

func runCheck(ctx context.Context, h CheckHandler, c CheckCall) (res *protocol.CheckResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	return h(ctx, c)
}

// quota answers a quota call: one quota.result or error frame.
func (s *server) quota(conn net.Conn, call protocol.Call) {
	ctx, cancel, ok := s.begin(conn)
	if !ok {
		return
	}
	defer cancel()
	ctx = withUpstreamInfo(ctx, call)
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	c := QuotaCall{ID: call.ID}
	if call.Account != nil {
		c.Account = *call.Account
	}
	var q *protocol.Quota
	var err error
	if s.ext.Quota == nil {
		err = &llm.Error{Code: llm.CodeUnsupported, Message: "this extension does not report quota", Scope: llm.ScopeRequest}
	} else {
		q, err = runQuota(ctx, s.ext.Quota, c)
	}
	if err == nil && q == nil {
		err = &llm.Error{Code: llm.CodeInternal, Message: "the extension reported no quota"}
	}
	if err == nil && q.Limits == nil {
		q.Limits = []protocol.QuotaLimit{}
	}
	reply := protocol.Reply{Event: llm.Event{Type: protocol.TypeQuotaResult}, Quota: q}
	if err != nil {
		reply = protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: asError(ctx, err)}}
	}
	_ = protocol.WriteFrame(conn, reply)
}

func runQuota(ctx context.Context, h QuotaHandler, c QuotaCall) (q *protocol.Quota, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	return h(ctx, c)
}

func runModels(ctx context.Context, h ModelsHandler, c ModelsCall) (list *protocol.ModelList, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &llm.Error{Code: llm.CodeInternal, Message: fmt.Sprintf("the extension panicked: %v", r)}
		}
	}()
	return h(ctx, c)
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

// StartBody starts the reply to an endpoint call: response.start with the reply's format (the
// call's endpoint format) and the body's content type. The body follows with Body.
func (s *Stream) StartBody(format, contentType string) error {
	raw, err := json.Marshal(llm.EndpointReply{ContentType: contentType})
	if err != nil {
		return err
	}
	return s.Start(llm.ResponseInfo{Format: format, Raw: raw})
}

// Body sends the next piece of an endpoint call's reply body, which is text: bytes that are not
// UTF-8 arrive as U+FFFD.
func (s *Stream) Body(chunk string) error {
	raw, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	return s.Send(llm.Event{Type: llm.EventNative, Raw: raw})
}

// BodyWriter returns a writer that sends what is written to it as body pieces (Body), each as
// soon as it is written, keeping a UTF-8 character that a write cuts in two for the next piece.
// Call its Close after the last write, before Finish, to send what it holds.
func (s *Stream) BodyWriter() *BodyWriter { return &BodyWriter{s: s} }

// BodyWriter sends an endpoint reply's body as it is written (see Stream.BodyWriter).
type BodyWriter struct {
	s    *Stream
	tail []byte // the start of a UTF-8 character the last write cut
}

func (w *BodyWriter) Write(p []byte) (int, error) {
	b := append(w.tail, p...)
	// Hold back the start of a character the write cut, at most 3 bytes.
	cut := len(b)
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				cut = i
			}
			break
		}
	}
	w.tail = append([]byte(nil), b[cut:]...)
	if cut > 0 {
		if err := w.s.Body(string(b[:cut])); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Close sends what the writer still holds.
func (w *BodyWriter) Close() error {
	if len(w.tail) == 0 {
		return nil
	}
	t := w.tail
	w.tail = nil
	return w.s.Body(string(t))
}

// Finish ends the reply successfully.
func (s *Stream) Finish(f llm.Finish, usage *llm.Usage) error {
	return s.Send(llm.Event{Type: llm.EventFinish, Finish: &f, Usage: usage})
}
