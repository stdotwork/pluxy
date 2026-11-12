package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// waitDelay bounds how long collecting an exited program waits for a child process of it that
// holds its stdout or stderr open.
const waitDelay = time.Second

// maxHandshakeLine bounds the handshake line, newline included.
const maxHandshakeLine = 4096

// Options tune how an extension is started. The zero value uses the defaults.
type Options struct {
	// Versions are the protocol versions core offers. Default: protocol.Version.
	Versions []int
	// StartTimeout bounds startup: the handshake line and describe. A failed start is killed
	// and collected before Start returns. Default 10s.
	StartTimeout time.Duration
	// StopTimeout bounds the wait for the program to exit after stdin closes. Default 5s.
	StopTimeout time.Duration
	// Log receives the program's stderr and later stdout, one prefixed line at a time, with
	// the credentials the extension holds removed (protocol.Redactor). Default os.Stderr.
	Log io.Writer
	// ProxyURL returns core's global proxy_url setting, read at each call: the proxy of every
	// account without one of its own (proxyurl.Resolve). Nil: none.
	ProxyURL func() string
	// SettingsSecrets returns the secret values in an account's settings, a JSON object. They
	// are removed from what every call on the account sends back and from the program's
	// output, as its credential is. Core passes configdoc.SettingsSecrets. Default: none.
	SettingsSecrets func(settings []byte) []string
}

// Extension is a running extension program.
type Extension struct {
	manifest *Manifest
	info     protocol.Info
	opts     Options
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	socket   string
	dir      string
	exited   chan struct{}
	waitErr  error
	calls    atomic.Uint64
	stopOnce sync.Once
	held     held
}

// held are the credentials core has given the extension or got back from it, so that its output
// is logged without them: for each account the latest two (after a refresh the extension may
// still write the one it replaced), the secrets of every settings it has had (a call still
// running after changes has old ones), and what a user entered during a login. Nothing is
// forgotten while the extension's program runs, so this grows only with the accounts it has
// served and their settings changes, and goes with the program.
//
// shortcut: a credential the extension creates (a refresh or login result) and writes out before
// core has it is logged as written; the redaction catches it only once the result arrives.
type held struct {
	mu       sync.Mutex
	by       map[string][]heldCredential
	out      map[string][]heldCredential // the proxy and header secrets of each account, apart from by so no account ID can name them
	settings map[string][]heldCredential // the secrets in each account's settings, apart in the same way
	redact   *protocol.Redactor
}

type heldCredential struct {
	key string // the credential as JSON, to tell whether it changed
	c   *protocol.Credential
}

// add records that the extension holds c for owner (an account ID), with the one before it.
func (h *held) add(owner string, c *protocol.Credential) { h.record(&h.by, owner, c, 2) }

// addOutbound records that the extension holds c, the proxy and header secrets of account, kept
// apart from the credentials add records so that no account ID can name the same entry.
func (h *held) addOutbound(account string, c *protocol.Credential) { h.record(&h.out, account, c, 2) }

// addSettings records that the extension holds c, the secrets in account's settings, kept apart
// in the same way, for as long as its program runs. This keeps one entry for each settings an
// account has had while the program runs; settings change only when an administrator changes
// them (a reload, PATCH /accounts), so that is how far it grows, and it goes with the program.
func (h *held) addSettings(account string, c *protocol.Credential) {
	h.record(&h.settings, account, c, 0)
}

// record records c for owner in m, keeping the latest n for owner, or all when n is 0.
func (h *held) record(m *map[string][]heldCredential, owner string, c *protocol.Credential, n int) {
	if c == nil {
		return
	}
	b, _ := json.Marshal(c)
	h.mu.Lock()
	defer h.mu.Unlock()
	list := (*m)[owner]
	if n == 0 && slices.ContainsFunc(list, func(hc heldCredential) bool { return hc.key == string(b) }) ||
		len(list) > 0 && list[len(list)-1].key == string(b) {
		return
	}
	if *m == nil {
		*m = map[string][]heldCredential{}
	}
	cc := *c
	list = append(list, heldCredential{string(b), &cc})
	if n > 0 {
		list = list[max(0, len(list)-n):]
	}
	(*m)[owner] = list
	var all []*protocol.Credential
	for _, by := range []map[string][]heldCredential{h.by, h.out, h.settings} {
		for _, l := range by {
			for _, hc := range l {
				all = append(all, hc.c)
			}
		}
	}
	h.redact = protocol.NewRedactor(all...)
}

// holdAccount records that the extension holds a's credential and the secrets in a's settings,
// and returns both as credentials, for the Redactor of a call on a.
func (e *Extension) holdAccount(a protocol.Account) []*protocol.Credential {
	e.held.add(a.ID, a.Credential)
	return []*protocol.Credential{a.Credential, e.holdSettings(a)}
}

// holdSettings records that the extension holds the secrets in a's settings
// (Options.SettingsSecrets), and returns them as a credential.
func (e *Extension) holdSettings(a protocol.Account) *protocol.Credential {
	if e.opts.SettingsSecrets == nil || len(a.Settings) == 0 {
		return nil
	}
	c := secretsCredential(e.opts.SettingsSecrets(a.Settings))
	e.held.addSettings(a.ID, c)
	return c
}

// String returns s without the credentials the extension holds.
func (h *held) String(s string) string {
	h.mu.Lock()
	r := h.redact
	h.mu.Unlock()
	return r.String(s)
}

// Start starts the extension described by the manifest at manifestPath, does the handshake and
// checks the extension's description against the manifest.
func Start(manifestPath string, opts Options) (*Extension, error) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	if len(opts.Versions) == 0 {
		opts.Versions = []int{protocol.Version}
	}
	if opts.StartTimeout == 0 {
		opts.StartTimeout = 10 * time.Second
	}
	if opts.StopTimeout == 0 {
		opts.StopTimeout = 5 * time.Second
	}
	if opts.Log == nil {
		opts.Log = os.Stderr
	}
	common := false
	for _, v := range m.Protocol {
		common = common || slices.Contains(opts.Versions, v)
	}
	if !common {
		return nil, fmt.Errorf("extension %s speaks protocol %v; core speaks %v", m.ID, m.Protocol, opts.Versions)
	}
	prog, err := m.Program()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "pluxy-ext-")
	if err != nil {
		return nil, err
	}
	e := &Extension{manifest: m, opts: opts, dir: dir, socket: dir + string(os.PathSeparator) + "ext.sock", exited: make(chan struct{})}
	deadline := time.Now().Add(opts.StartTimeout)
	if err := e.start(prog, deadline); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := e.describe(deadline); err != nil {
		e.abort()
		return nil, err
	}
	return e, nil
}

// abort kills a program that failed to start and waits until it is collected.
func (e *Extension) abort() {
	e.Kill()
	<-e.exited
}

func (e *Extension) start(prog string, deadline time.Time) error {
	versions := make([]string, len(e.opts.Versions))
	for i, v := range e.opts.Versions {
		versions[i] = strconv.Itoa(v)
	}
	cmd := exec.Command(prog)
	cmd.Dir = e.manifest.dir
	// A cleared environment: the extension gets only what it needs to run.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		protocol.EnvProtocolVersions + "=" + strings.Join(versions, ","),
		protocol.EnvSocket + "=" + e.socket,
		protocol.EnvExtensionID + "=" + e.manifest.ID,
	}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
	}
	if v := os.Getenv(protocol.EnvTestLoginHosts); v != "" {
		cmd.Env = append(cmd.Env, protocol.EnvTestLoginHosts+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = &lineLogger{w: e.opts.Log, prefix: "[" + e.manifest.ID + "] ", redact: e.held.String}
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting extension %s: %w", e.manifest.ID, err)
	}
	e.cmd, e.stdin = cmd, stdin

	type handshake struct {
		line string
		err  error
	}
	lines := make(chan handshake, 1)
	go func() {
		r := bufio.NewReaderSize(stdout, maxHandshakeLine)
		line, err := r.ReadSlice('\n')
		lines <- handshake{string(line), err}
		// Keep draining stdout so the program never blocks on it.
		_, _ = io.Copy(&lineLogger{w: e.opts.Log, prefix: "[" + e.manifest.ID + " stdout] ", redact: e.held.String}, r)
	}()
	go func() {
		e.waitErr = cmd.Wait()
		close(e.exited)
		os.RemoveAll(e.dir)
	}()

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case h := <-lines:
		switch {
		case h.err == nil:
			if err := e.checkHandshake(h.line); err != nil {
				e.abort()
				return err
			}
			return nil
		case errors.Is(h.err, bufio.ErrBufferFull):
			e.abort()
			return fmt.Errorf("extension %s: handshake line is longer than %d bytes", e.manifest.ID, maxHandshakeLine)
		}
		// stdout ended without a handshake: wait for the program to exit, within the deadline.
		select {
		case <-e.exited:
			return fmt.Errorf("extension %s exited before its handshake: %v", e.manifest.ID, e.waitErr)
		case <-timer.C:
			e.abort()
			return fmt.Errorf("extension %s closed its stdout without a handshake and did not exit within %s", e.manifest.ID, e.opts.StartTimeout)
		}
	case <-timer.C:
		e.abort()
		return fmt.Errorf("extension %s sent no handshake within %s", e.manifest.ID, e.opts.StartTimeout)
	}
}

func (e *Extension) checkHandshake(line string) error {
	version, _, addr, err := protocol.ParseHandshake(line)
	if err == nil && !slices.Contains(e.opts.Versions, version) {
		err = fmt.Errorf("extension chose protocol %d, which core did not offer", version)
	}
	if err == nil && addr != e.socket {
		err = fmt.Errorf("extension listens on %q, not on the socket core gave it", addr)
	}
	if err != nil {
		return fmt.Errorf("extension %s: %w", e.manifest.ID, err)
	}
	return nil
}

func (e *Extension) describe(deadline time.Time) error {
	d := net.Dialer{Deadline: deadline}
	conn, err := d.Dial("unix", e.socket)
	if err != nil {
		return fmt.Errorf("extension %s: describe: %w", e.manifest.ID, err)
	}
	defer conn.Close()
	// describe is part of starting up, so the startup deadline covers it.
	_ = conn.SetDeadline(deadline)
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpDescribe}); err != nil {
		return fmt.Errorf("extension %s: describe: %w", e.manifest.ID, err)
	}
	var reply protocol.Reply
	if err := protocol.ReadFrame(conn, &reply); err != nil {
		return fmt.Errorf("extension %s: describe: %w", e.manifest.ID, err)
	}
	if reply.Type != protocol.TypeDescribeResult || reply.Info == nil {
		return fmt.Errorf("extension %s: describe returned %q", e.manifest.ID, reply.Type)
	}
	if reply.Info.ID != e.manifest.ID {
		return fmt.Errorf("extension %s describes itself as %q", e.manifest.ID, reply.Info.ID)
	}
	e.info = *reply.Info
	return nil
}

func (e *Extension) nextID() string {
	return "c-" + strconv.FormatUint(e.calls.Add(1), 10)
}

// Manifest returns the extension's manifest.
func (e *Extension) Manifest() *Manifest { return e.manifest }

// Info returns the extension's reply to describe.
func (e *Extension) Info() protocol.Info { return e.info }

// Exited is closed when the program has exited.
func (e *Extension) Exited() <-chan struct{} { return e.exited }

// ExitError is the program's exit result once Exited is closed: nil for a clean exit.
func (e *Extension) ExitError() error {
	<-e.exited
	return e.waitErr
}

// PID returns the program's process ID.
func (e *Extension) PID() int {
	if e.cmd == nil || e.cmd.Process == nil {
		return 0
	}
	return e.cmd.Process.Pid
}

// Kill kills the program without waiting.
func (e *Extension) Kill() {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
}

// Stop asks the program to exit by closing its stdin, and kills it if it has not exited within
// the stop timeout. It returns once the program has exited.
func (e *Extension) Stop() {
	e.stopOnce.Do(func() {
		if e.stdin != nil {
			e.stdin.Close()
		}
		select {
		case <-e.exited:
		case <-time.After(e.opts.StopTimeout):
			e.Kill()
			<-e.exited
		}
	})
	<-e.exited
}

// Generate starts a generate call. The call is cancelled when ctx ends or the stream is closed.
// Every event the call returns has the account's credential and the secrets in its settings
// removed (protocol.Redactor, Options.SettingsSecrets), so nothing the extension or its upstream
// echoes reaches a client or a log with them.
func (e *Extension) Generate(ctx context.Context, account protocol.Account, req llm.Request) *Stream {
	return e.call(ctx, protocol.OpGenerate, account, req)
}

// CountsTokens reports whether an extension that described itself with info counts tokens: it
// implements minor version 2 or later and lists the count_tokens operation.
func CountsTokens(info protocol.Info) bool {
	return info.ProtocolMinor >= 2 && slices.Contains(info.Operations, protocol.OpCountTokens)
}

// CountTokens starts a count_tokens call, answered as a generate call is (see Generate). An
// extension that does not count tokens answers at once with an unsupported error about the
// account, so routing moves on to another account.
func (e *Extension) CountTokens(ctx context.Context, account protocol.Account, req llm.Request) *Stream {
	if !CountsTokens(e.info) {
		return cannotCount(e.manifest.ID)
	}
	return e.call(ctx, protocol.OpCountTokens, account, req)
}

// ServesEndpoints reports whether an extension that described itself with info serves endpoint
// calls: it implements minor version 7 or later and lists the endpoint operation.
func ServesEndpoints(info protocol.Info) bool {
	return info.ProtocolMinor >= 7 && slices.Contains(info.Operations, protocol.OpEndpoint)
}

// Endpoint starts an endpoint call, answered as a generate call is (see Generate). The reply's
// body is redacted as one text, also where a secret is cut between its pieces
// (protocol.BodyRedactor). An extension that serves no endpoints answers at once with an
// unsupported error about the account, so routing moves on to another account.
func (e *Extension) Endpoint(ctx context.Context, account protocol.Account, req llm.Request) *Stream {
	if !ServesEndpoints(e.info) {
		return servesNoEndpoints(e.manifest.ID, req)
	}
	return e.call(ctx, protocol.OpEndpoint, account, req)
}

// servesNoEndpoints is the reply of an extension that does not serve req's endpoint.
func servesNoEndpoints(id string, req llm.Request) *Stream {
	name := ""
	if req.Native != nil {
		name = req.Native.Format
	}
	s := &Stream{}
	s.fail(&llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("extension %s does not serve %s", id, name)})
	return s
}

// cannotCount is the reply of an extension that does not count tokens.
func cannotCount(id string) *Stream {
	s := &Stream{}
	s.fail(&llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("extension %s does not count tokens", id)})
	return s
}

// call starts a generate, count_tokens or endpoint call.
func (e *Extension) call(ctx context.Context, op string, account protocol.Account, req llm.Request) *Stream {
	select {
	case <-e.exited:
		return failed(llm.CodeExtensionUnavailable, fmt.Sprintf("extension %s is not running", e.manifest.ID))
	default:
	}
	proxy, perr := e.outbound(&account)
	if perr != nil {
		s := &Stream{}
		s.fail(perr)
		return s
	}
	redact := protocol.NewRedactor(append(e.holdAccount(account), proxy)...)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", e.socket)
	if err != nil {
		if ctx.Err() != nil {
			return failed(llm.CodeCancelled, "the request was cancelled")
		}
		return failed(llm.CodeExtensionUnavailable, fmt.Sprintf("extension %s is not reachable: %v", e.manifest.ID, err))
	}
	s := &Stream{conn: conn, ctx: ctx, ext: e, redact: redact, capture: capture(ctx, e.info), account: account.ID}
	if op == protocol.OpEndpoint {
		s.body = redact.Body()
	}
	s.stop = context.AfterFunc(ctx, func() { conn.Close() })
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: op, Account: &account, Request: &req, Capture: s.capture != nil}
	if err := protocol.WriteFrame(conn, call); err != nil {
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			// The request is valid but too big to pass on in one frame; only a smaller request
			// can fix that.
			s.fail(&llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusRequestEntityTooLarge, Scope: llm.ScopeRequest,
				Message: "request is too large to pass to the provider extension"})
			return s
		}
		s.fail(s.readError(err))
	}
	return s
}

// Stream is the reply to a generate call. Next returns events until and including exactly one
// terminal event (finish or error), then keeps returning that terminal event.
type Stream struct {
	conn   net.Conn
	ctx    context.Context
	ext    *Extension
	redact *protocol.Redactor
	// body is an endpoint call's: it redacts the reply's body across its pieces, holding back
	// the end of a piece that could be the start of a secret (protocol.BodyRedactor). Its secrets
	// are redact's, those the call was sent with; a refresh or a settings change while the reply
	// runs reaches later calls, not this one, so they do not change mid-body.
	body  *protocol.BodyRedactor
	stop  func() bool
	seq   llm.Sequence
	final *llm.Event
	// onClose ends the call on its managed generation; safe to call more than once.
	onClose func()
	// capture receives the call's diagnostic frames; nil when the call is not captured.
	capture CaptureFunc
	account string
}

func failed(code, msg string) *Stream {
	s := &Stream{}
	s.fail(&llm.Error{Code: code, Message: msg})
	return s
}

func (s *Stream) fail(e *llm.Error) {
	ev := llm.Event{Type: llm.EventError, Error: e}
	s.final = &ev
	s.Close()
}

// Next returns the next event.
func (s *Stream) Next() llm.Event {
	if s.final != nil {
		return s.last()
	}
	var reply protocol.Reply
	if err := protocol.ReadFrame(s.conn, &reply); err != nil {
		s.fail(s.readError(err))
		return s.last()
	}
	for reply.Type == protocol.TypeDiagnostic {
		if s.capture != nil && reply.Diagnostic != nil {
			s.capture(s.account, redactDiagnostic(s.redact, *reply.Diagnostic))
		}
		reply = protocol.Reply{}
		if err := protocol.ReadFrame(s.conn, &reply); err != nil {
			s.fail(s.readError(err))
			return s.last()
		}
	}
	if err := s.seq.Check(reply.Event); err != nil {
		s.fail(redacted(s.redact, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s broke the protocol: %v", s.ext.manifest.ID, err)}))
		return s.last()
	}
	var piece string
	if s.body != nil && reply.Event.Type == llm.EventNative && json.Unmarshal(reply.Event.Raw, &piece) == nil {
		// A piece of an endpoint reply's body, redacted with what came before it. A piece that is
		// not a JSON string is redacted on its own below, for the server to refuse.
		ev := reply.Event
		ev.Raw = nil
		ev = s.redact.Event(ev)
		if out := s.body.Write(piece); out != piece {
			ev.Raw = bodyPiece(out)
		} else {
			ev.Raw = reply.Event.Raw
		}
		return ev
	}
	ev := s.redact.Event(reply.Event)
	if ev.Terminal() {
		s.final = &ev
		s.Close()
		return s.last()
	}
	return ev
}

// last returns what Next returns once the call has ended: first the end of an endpoint reply's
// body that was held back, if any, when the body ended with finish, then the terminal event,
// again and again. A body that breaks off (an error, a cancel) drops what was held back: it was
// held only because it could be the start of a secret, and the body is broken there anyway.
func (s *Stream) last() llm.Event {
	if rest := s.body.Flush(); rest != "" && s.final.Type == llm.EventFinish {
		return llm.Event{Type: llm.EventNative, Raw: bodyPiece(rest)}
	}
	return *s.final
}

// bodyPiece returns a body piece as the JSON string a native event carries. BodyRedactor cuts
// a body only where a form of a secret starts, which is where a character starts, so the piece
// is valid UTF-8 as the decoded string it came from was, and encodes exactly.
func bodyPiece(text string) json.RawMessage {
	b, _ := json.Marshal(text)
	return b
}

func (s *Stream) readError(err error) *llm.Error {
	if s.ctx != nil && s.ctx.Err() != nil {
		return &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
	}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.Is(err, protocol.ErrFrameTooLarge) || errors.As(err, &se) || errors.As(err, &te) {
		return redacted(s.redact, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a bad frame: %v", s.ext.manifest.ID, err)})
	}
	return &llm.Error{Code: llm.CodeExtensionCrashed, Message: fmt.Sprintf("extension %s ended the call without finishing it", s.ext.manifest.ID)}
}

// Close ends the call; if it had not finished, the extension sees it cancelled.
func (s *Stream) Close() {
	if s.onClose != nil {
		defer s.onClose()
	}
	if s.stop != nil {
		s.stop()
	}
	if s.conn != nil {
		s.conn.Close()
	}
}

// maxLogLine bounds a line of extension output that is logged.
const maxLogLine = 64 << 10

// lineLogger writes whole lines to w with a prefix, each passed through redact. A line longer
// than maxLogLine is left out, with a note: cut into parts, a credential split between two of
// them would not be recognised.
type lineLogger struct {
	mu     sync.Mutex
	w      io.Writer
	prefix string
	redact func(string) string
	buf    []byte
	skip   bool // dropping the rest of a line longer than maxLogLine
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		switch {
		case l.skip:
		case i > maxLogLine:
			l.longLine()
		default:
			fmt.Fprintf(l.w, "%s%s\n", l.prefix, l.redact(string(l.buf[:i])))
		}
		l.skip = false
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > maxLogLine { // never hold an unbounded partial line
		if !l.skip {
			l.longLine()
		}
		l.skip = true
		l.buf = l.buf[:0]
	}
	return len(p), nil
}

func (l *lineLogger) longLine() {
	fmt.Fprintf(l.w, "%s(a line longer than %d bytes, not logged)\n", l.prefix, maxLogLine)
}
