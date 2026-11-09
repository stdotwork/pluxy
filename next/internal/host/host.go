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
// still write the one it replaced), and what a user entered during a login. Accounts are not
// forgotten while the extension runs, so this grows only with the accounts it has served.
//
// shortcut: a credential the extension creates (a refresh or login result) and writes out before
// core has it is logged as written; the redaction catches it only once the result arrives.
type held struct {
	mu     sync.Mutex
	by     map[string][]heldCredential
	redact *protocol.Redactor
}

type heldCredential struct {
	key string // the credential as JSON, to tell whether it changed
	c   *protocol.Credential
}

// add records that the extension holds c for owner (an account ID).
func (h *held) add(owner string, c *protocol.Credential) {
	if c == nil {
		return
	}
	b, _ := json.Marshal(c)
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.by[owner]
	if len(list) > 0 && list[len(list)-1].key == string(b) {
		return
	}
	if h.by == nil {
		h.by = map[string][]heldCredential{}
	}
	cc := *c
	list = append(list, heldCredential{string(b), &cc})
	h.by[owner] = list[max(0, len(list)-2):]
	var all []*protocol.Credential
	for _, l := range h.by {
		for _, hc := range l {
			all = append(all, hc.c)
		}
	}
	h.redact = protocol.NewRedactor(all...)
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
// Every event the call returns has the account's credential removed (protocol.Redactor), so
// nothing the extension or its upstream echoes reaches a client or a log with it.
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

// cannotCount is the reply of an extension that does not count tokens.
func cannotCount(id string) *Stream {
	s := &Stream{}
	s.fail(&llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("extension %s does not count tokens", id)})
	return s
}

// call starts a generate or count_tokens call.
func (e *Extension) call(ctx context.Context, op string, account protocol.Account, req llm.Request) *Stream {
	select {
	case <-e.exited:
		return failed(llm.CodeExtensionUnavailable, fmt.Sprintf("extension %s is not running", e.manifest.ID))
	default:
	}
	e.held.add(account.ID, account.Credential)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", e.socket)
	if err != nil {
		if ctx.Err() != nil {
			return failed(llm.CodeCancelled, "the request was cancelled")
		}
		return failed(llm.CodeExtensionUnavailable, fmt.Sprintf("extension %s is not reachable: %v", e.manifest.ID, err))
	}
	s := &Stream{conn: conn, ctx: ctx, ext: e, redact: protocol.NewRedactor(account.Credential)}
	s.stop = context.AfterFunc(ctx, func() { conn.Close() })
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: op, Account: &account, Request: &req}
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
	stop   func() bool
	seq    llm.Sequence
	final  *llm.Event
	// onClose ends the call on its managed generation; safe to call more than once.
	onClose func()
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
		return *s.final
	}
	var reply protocol.Reply
	if err := protocol.ReadFrame(s.conn, &reply); err != nil {
		s.fail(s.readError(err))
		return *s.final
	}
	if err := s.seq.Check(reply.Event); err != nil {
		s.fail(&llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s broke the protocol: %v", s.ext.manifest.ID, err)})
		return *s.final
	}
	ev := s.redact.Event(reply.Event)
	if ev.Terminal() {
		s.final = &ev
		s.Close()
	}
	return ev
}

func (s *Stream) readError(err error) *llm.Error {
	if s.ctx != nil && s.ctx.Err() != nil {
		return &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
	}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.Is(err, protocol.ErrFrameTooLarge) || errors.As(err, &se) || errors.As(err, &te) {
		return &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a bad frame: %v", s.ext.manifest.ID, err)}
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
