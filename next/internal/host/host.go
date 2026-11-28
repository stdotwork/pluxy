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

	"github.com/stdotwork/pluxy/next/format"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/protocol/nativeonly"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

const waitDelay = time.Second

const maxHandshakeLine = 4096

type Options struct {
	Versions        []int
	StartTimeout    time.Duration
	StopTimeout     time.Duration
	Log             io.Writer
	ProxyURL        func() string
	Timeouts        func() *protocol.Timeouts
	Payload         func() *payload.Rules
	SettingsSecrets func(settings []byte) []string

	beforeHandshakeWait func(deadline time.Time)
	beforeProgramStart  func()
}

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
	out      output

	startTook time.Duration
}

type held struct {
	mu       sync.Mutex
	by       map[string][]heldCredential
	out      map[string][]heldCredential
	settings map[string][]heldCredential
	pins     int
	replaced []*protocol.Credential
	overflow bool
	redact   *protocol.Redactor
}

const maxReplaced = 256

type heldCredential struct {
	key string
	c   *protocol.Credential
}

func (h *held) add(owner string, c *protocol.Credential) { h.record(&h.by, owner, c, 2) }

func (h *held) addOutbound(account string, c *protocol.Credential) { h.record(&h.out, account, c, 2) }

func (h *held) addSettings(account string, c *protocol.Credential) {
	h.record(&h.settings, account, c, 0)
}

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
	if n > 0 && len(list) > n {
		if h.pins > 0 && !h.overflow {
			for _, hc := range list[:len(list)-n] {
				h.replaced = append(h.replaced, hc.c)
			}
			if len(h.replaced) > maxReplaced {
				h.replaced, h.overflow = nil, true
			}
		}
		list = list[len(list)-n:]
	}
	(*m)[owner] = list
	h.rebuild()
}

func (h *held) rebuild() {
	all := slices.Clone(h.replaced)
	for _, by := range []map[string][]heldCredential{h.by, h.out, h.settings} {
		for _, l := range by {
			for _, hc := range l {
				all = append(all, hc.c)
			}
		}
	}
	h.redact = protocol.NewRedactor(all...)
}

func (h *held) pin() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pins++
}

func (h *held) unpin() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pins--; h.pins == 0 {
		h.overflow = false
		if h.replaced != nil {
			h.replaced = nil
			h.rebuild()
		}
	}
}

func (h *held) redactor() (*protocol.Redactor, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.redact, h.overflow
}

func (e *Extension) holdAccount(a protocol.Account) []*protocol.Credential {
	e.held.add(a.ID, a.Credential)
	return []*protocol.Credential{a.Credential, e.holdSettings(a)}
}

func (e *Extension) holdSettings(a protocol.Account) *protocol.Credential {
	if e.opts.SettingsSecrets == nil || len(a.Settings) == 0 {
		return nil
	}
	c := secretsCredential(e.opts.SettingsSecrets(a.Settings))
	e.held.addSettings(a.ID, c)
	return c
}

func (h *held) String(s string) string {
	h.mu.Lock()
	r := h.redact
	h.mu.Unlock()
	return r.String(s)
}

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
	deadline, err := e.start(prog)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := e.describe(deadline); err != nil {
		e.abort()
		return nil, err
	}
	return e, nil
}

func (e *Extension) abort() {
	e.Kill()
	<-e.exited
}

// A new program a virus scanner reads first can take longer than the limit to start, and the start
// itself cannot be stopped.
func (e *Extension) start(prog string) (time.Time, error) {
	began := time.Now()
	versions := make([]string, len(e.opts.Versions))
	for i, v := range e.opts.Versions {
		versions[i] = strconv.Itoa(v)
	}
	cmd := exec.Command(prog)
	cmd.Dir = e.manifest.dir
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
		return time.Time{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return time.Time{}, err
	}
	e.out = output{w: e.opts.Log, held: &e.held, prefix: "[" + e.manifest.ID + "] "}
	cmd.Stderr = &lineLogger{out: &e.out, prefix: "[" + e.manifest.ID + "] "}
	cmd.WaitDelay = waitDelay
	if e.opts.beforeProgramStart != nil {
		e.opts.beforeProgramStart()
	}
	if err := cmd.Start(); err != nil {
		return time.Time{}, fmt.Errorf("starting extension %s: %w", e.manifest.ID, err)
	}
	e.cmd, e.stdin = cmd, stdin
	e.startTook = time.Since(began)
	deadline := time.Now().Add(e.opts.StartTimeout)

	type handshake struct {
		line string
		err  error
	}
	lines := make(chan handshake, 1)
	go func() {
		r := bufio.NewReaderSize(stdout, maxHandshakeLine)
		line, err := r.ReadSlice('\n')
		lines <- handshake{string(line), err}
		_, _ = io.Copy(&lineLogger{out: &e.out, prefix: "[" + e.manifest.ID + " stdout] "}, r)
	}()
	go func() {
		e.waitErr = cmd.Wait()
		close(e.exited)
		os.RemoveAll(e.dir)
	}()

	if e.opts.beforeHandshakeWait != nil {
		e.opts.beforeHandshakeWait(deadline)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case h := <-lines:
		if h.err == nil && !time.Now().Before(deadline) {
			err := e.startLimitError("sent no handshake", "readable, read after the limit")
			e.abort()
			return time.Time{}, err
		}
		switch {
		case h.err == nil:
			if err := e.checkHandshake(h.line); err != nil {
				e.abort()
				return time.Time{}, err
			}
			return deadline, nil
		case errors.Is(h.err, bufio.ErrBufferFull):
			e.abort()
			return time.Time{}, fmt.Errorf("extension %s: handshake line is longer than %d bytes", e.manifest.ID, maxHandshakeLine)
		}
		select {
		case <-e.exited:
			return time.Time{}, fmt.Errorf("extension %s exited before its handshake: %v", e.manifest.ID, e.waitErr)
		case <-timer.C:
			err := e.startLimitError("closed its stdout without a handshake and did not exit", "not readable, stdout closed")
			e.abort()
			return time.Time{}, err
		}
	case <-timer.C:
		handshake := "not read"
		select {
		case h := <-lines:
			if h.err == nil {
				handshake = "readable, read after the limit"
			} else {
				handshake = "not readable, stdout closed"
			}
		default:
			if f, ok := stdout.(*os.File); ok {
				if n, ok := unreadBytes(f); ok && n > 0 {
					handshake = fmt.Sprintf("not read, %d bytes waiting in its stdout", n)
				} else if ok {
					handshake = "not read, no bytes waiting in its stdout"
				}
			}
		}
		err := e.startLimitError("sent no handshake", handshake)
		e.abort()
		return time.Time{}, err
	}
}

func (e *Extension) startLimitError(what, handshake string) error {
	details := append([]string{"starting its process took " + e.startTook.String(), "handshake: " + handshake}, processState(e.PID())...)
	return fmt.Errorf("extension %s %s within %s (%s)", e.manifest.ID, what, e.opts.StartTimeout, strings.Join(details, "; "))
}

func processState(pid int) []string {
	if pid <= 0 {
		return nil
	}
	var out []string
	proc := "/proc/" + strconv.Itoa(pid) + "/"
	if b, err := os.ReadFile(proc + "stat"); err == nil {
		if i := bytes.LastIndexByte(b, ')'); i >= 0 {
			if f := strings.Fields(string(b[i+1:])); len(f) > 0 {
				out = append(out, "process state "+f[0])
			}
		}
	}
	if b, err := os.ReadFile(proc + "wchan"); err == nil {
		if w := strings.TrimSpace(string(b)); w != "" {
			out = append(out, "wchan "+w)
		}
	}
	return out
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
	err := e.describeCall(deadline)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return e.startLimitError("did not answer describe", "read in time")
	}
	return err
}

func (e *Extension) describeCall(deadline time.Time) error {
	d := net.Dialer{Deadline: deadline}
	conn, err := d.Dial("unix", e.socket)
	if err != nil {
		return fmt.Errorf("extension %s: describe: %w", e.manifest.ID, err)
	}
	defer conn.Close()
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

func (e *Extension) Manifest() *Manifest { return e.manifest }

func (e *Extension) Info() protocol.Info { return e.info }

func (e *Extension) Exited() <-chan struct{} { return e.exited }

func (e *Extension) ExitError() error {
	<-e.exited
	return e.waitErr
}

func (e *Extension) PID() int {
	if e.cmd == nil || e.cmd.Process == nil {
		return 0
	}
	return e.cmd.Process.Pid
}

func (e *Extension) Kill() {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
}

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

func (e *Extension) Generate(ctx context.Context, account protocol.Account, req llm.Request) *Stream {
	return e.call(ctx, protocol.OpGenerate, account, req)
}

func CountsTokens(info protocol.Info) bool {
	return info.ProtocolMinor >= 2 && slices.Contains(info.Operations, protocol.OpCountTokens)
}

func (e *Extension) CountTokens(ctx context.Context, account protocol.Account, req llm.Request) *Stream {
	if !CountsTokens(e.info) {
		return cannotCount(e.manifest.ID)
	}
	return e.call(ctx, protocol.OpCountTokens, account, req)
}

const nativeOnlyMinor = 27

func ParsesNative(info protocol.Info) bool {
	return info.ProtocolMinor >= nativeOnlyMinor && info.NativeParser == format.ParserVersion
}

func ServesEndpoints(info protocol.Info) bool {
	return info.ProtocolMinor >= 7 && slices.Contains(info.Operations, protocol.OpEndpoint)
}

func (e *Extension) Endpoint(ctx context.Context, account protocol.Account, req llm.Request) *Stream {
	if !ServesEndpoints(e.info) {
		return servesNoEndpoints(e.manifest.ID, req)
	}
	if req.Native != nil && (req.Native.Format == llm.FormatGeminiEmbedContent || req.Native.Format == llm.FormatGeminiBatchEmbedContents) && e.info.ProtocolMinor < GeminiEmbeddingsMinor {
		return servesNoEndpoints(e.manifest.ID, req)
	}
	return e.call(ctx, protocol.OpEndpoint, account, req)
}

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

func cannotCount(id string) *Stream {
	s := &Stream{}
	s.fail(&llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("extension %s does not count tokens", id)})
	return s
}

func (e *Extension) call(ctx context.Context, op string, account protocol.Account, req llm.Request) *Stream {
	select {
	case <-e.exited:
		return failed(llm.CodeExtensionUnavailable, fmt.Sprintf("extension %s is not running", e.manifest.ID))
	default:
	}
	proxy, perr := e.outbound(&account, clientHeaders(ctx))
	if perr != nil {
		s := &Stream{}
		s.fail(perr)
		return s
	}
	e.resolveTimeouts(&account)
	rules, perr := e.payload(ctx, req, payloadMinor)
	if perr != nil {
		s := &Stream{}
		s.fail(perr)
		return s
	}
	req, lost := downgradeToolResults(e.info.ProtocolMinor, e.manifest.ID, req)
	if perr := refuseDowngrade(e.manifest.ID, e.info.ProtocolMinor, req, lost); perr != nil {
		s := &Stream{}
		s.fail(perr)
		return s
	}
	req, allowedErr := downgradeAllowedTools(e.info.ProtocolMinor, req)
	if allowedErr != nil {
		s := &Stream{}
		s.fail(allowedErr)
		return s
	}
	req, tierLost := downgradeServiceTier(e.info.ProtocolMinor, e.manifest.ID, req)
	if perr := refuseServiceTier(e.manifest.ID, e.info.ProtocolMinor, req, tierLost); perr != nil {
		s := &Stream{}
		s.fail(perr)
		return s
	}
	lost = append(lost, tierLost...)
	req, candLost := downgradeCandidateCount(e.info.ProtocolMinor, e.manifest.ID, req)
	if perr := refuseCandidateCount(e.manifest.ID, e.info.ProtocolMinor, req, candLost); perr != nil {
		s := &Stream{}
		s.fail(perr)
		return s
	}
	lost = append(lost, candLost...)
	req = downgradeWebSearch(e.info.ProtocolMinor, req)
	req = downgradeCodeExecution(e.info.ProtocolMinor, req)
	redact := protocol.NewRedactor(append(e.holdAccount(account), proxy)...)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", e.socket)
	if err != nil {
		if ctx.Err() != nil {
			return failed(llm.CodeCancelled, "the request was cancelled")
		}
		return failed(llm.CodeExtensionUnavailable, fmt.Sprintf("extension %s is not reachable: %v", e.manifest.ID, err))
	}
	s := &Stream{conn: conn, ctx: ctx, ext: e, redact: redact, pieces: e.accountPieces(account), capture: capture(ctx, e.info), account: account.ID, lost: lost}
	alive := e.info.ProtocolMinor >= AliveMinor
	if alive {
		s.watch = watchdog(account.Timeouts, req.Stream)
	}
	if op == protocol.OpEndpoint {
		s.body = redact.Body()
	}
	s.stop = context.AfterFunc(ctx, func() { conn.Close() })
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: op, Account: &account, Request: &req, Capture: s.capture != nil, Payload: rules, Alive: alive}
	if op == protocol.OpGenerate && ParsesNative(e.info) && nativeonly.Can(req) {
		sent := nativeonly.Strip(req)
		call.Request, call.NativeOnly = &sent, true
	}
	if err := protocol.WriteFrame(conn, call); err != nil {
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			s.fail(&llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusRequestEntityTooLarge, Scope: llm.ScopeRequest,
				Message: "request is too large to pass to the provider extension"})
			return s
		}
		s.fail(s.readError(err))
	}
	return s
}

type Stream struct {
	conn    net.Conn
	frames  *protocol.FrameReader
	ctx     context.Context
	ext     *Extension
	redact  *protocol.Redactor
	body    *protocol.BodyRedactor
	stop    func() bool
	seq     llm.Sequence
	final   *llm.Event
	onClose func()
	capture CaptureFunc
	account string
	watch   time.Duration
	started bool
	alives  atomic.Int64
	closed  atomic.Bool
	pieces  pieces
	held    heldCapture
	lost    []llm.Loss
}

func (s *Stream) Alives() int { return int(s.alives.Load()) }

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

func (s *Stream) Next() llm.Event {
	if s.final != nil {
		return s.last()
	}
	var reply protocol.Reply
	for {
		if err := s.read(&reply); err != nil {
			s.fail(s.readError(err))
			return s.last()
		}
		if reply.Type == protocol.TypeAlive {
			s.alives.Add(1)
		} else if reply.Type != protocol.TypeDiagnostic {
			break
		} else if s.capture != nil && reply.Diagnostic != nil {
			s.held.hold(redactDiagnostic(s.redact, *reply.Diagnostic))
		}
		reply = protocol.Reply{}
	}
	if err := s.seq.Check(reply.Event); err != nil {
		s.fail(redacted(s.redact, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s broke the protocol: %v", s.ext.manifest.ID, err)}))
		return s.last()
	}
	if reply.Type == llm.EventResponseStart {
		s.started = true
	}
	var piece string
	if s.body != nil && reply.Event.Type == llm.EventNative && json.Unmarshal(reply.Event.Raw, &piece) == nil {
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
	ev := withLosses(s.ext.normalizeEvent(s.pieces.event(s.redact.Event(reply.Event))), s.lost)
	if ev.Terminal() {
		if ev.Type == llm.EventFinish {
			s.held.succeed()
		}
		s.final = &ev
		s.Close()
		return s.last()
	}
	return ev
}

func (s *Stream) last() llm.Event {
	if rest := s.body.Flush(); rest != "" && s.final.Type == llm.EventFinish {
		return llm.Event{Type: llm.EventNative, Raw: bodyPiece(rest)}
	}
	return *s.final
}

func bodyPiece(text string) json.RawMessage {
	b, _ := json.Marshal(text)
	return b
}

func (s *Stream) read(reply *protocol.Reply) error {
	if s.closed.Load() || (s.ctx != nil && s.ctx.Err() != nil) {
		return net.ErrClosed
	}
	if s.watch > 0 {
		_ = s.conn.SetReadDeadline(time.Now().Add(s.watch))
	}
	if s.frames == nil {
		s.frames = protocol.NewFrameReader(s.conn)
	}
	return s.frames.Read(reply)
}

func (s *Stream) readError(err error) *llm.Error {
	if s.ctx != nil && s.ctx.Err() != nil {
		return &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
	}
	if s.watch > 0 && errors.Is(err, os.ErrDeadlineExceeded) {
		return s.ext.timedOut(s.account, s.watch, s.started)
	}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.Is(err, protocol.ErrFrameTooLarge) || errors.As(err, &se) || errors.As(err, &te) {
		return redacted(s.redact, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a bad frame: %v", s.ext.manifest.ID, err)})
	}
	return &llm.Error{Code: llm.CodeExtensionCrashed, Message: fmt.Sprintf("extension %s ended the call without finishing it", s.ext.manifest.ID)}
}

func (s *Stream) Close() {
	s.closed.Store(true)
	if s.onClose != nil {
		defer s.onClose()
	}
	ds, failed := s.held.take()
	for _, d := range ds {
		s.capture(s.account, s.pieces.diagnostic(d, failed))
	}
	if s.stop != nil {
		s.stop()
	}
	if s.conn != nil {
		s.conn.Close()
	}
}

const maxLogLine = 64 << 10

const maxHeldOutput = 1 << 20

const heldLineCost = 64

type output struct {
	mu      sync.Mutex
	w       io.Writer
	held    *held
	prefix  string
	minting int
	queue   []heldLine
	size    int
	dropped int
}

type heldLine struct{ prefix, text string }

func (o *output) line(prefix, text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch cost := len(text) + heldLineCost; {
	case o.minting == 0:
		o.write(prefix, text)
	case o.dropped > 0:
		o.dropped++
	case o.overflowed():
		o.dropQueue()
		o.leaveOut(1, overflowNote)
	case o.size+cost > maxHeldOutput:
		o.leaveOut(1, sizeNote)
	default:
		o.queue = append(o.queue, heldLine{prefix, text})
		o.size += cost
	}
}

var (
	sizeNote = fmt.Sprintf("the output held while a refresh or login runs passed its limit of %d bytes, counting %d for each line besides its text; the rest of the held output is left out until it ends",
		maxHeldOutput, heldLineCost)
	overflowNote = fmt.Sprintf("more than %d credentials were replaced while a refresh or login runs; all of the held output is left out until it ends", maxReplaced)
)

func (o *output) overflowed() bool {
	_, overflow := o.held.redactor()
	return overflow
}

func (o *output) leaveOut(n int, note string) {
	if o.dropped == 0 {
		o.note(note)
	}
	o.dropped += n
}

func (o *output) note(note string) { fmt.Fprintf(o.w, "%s(%s)\n", o.prefix, note) }

func (o *output) core(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.w, "%s%s\n", o.prefix, text)
}

func (o *output) dropQueue() {
	if len(o.queue) > 0 {
		if o.dropped > 0 {
			o.note(overflowNote)
		}
		o.leaveOut(len(o.queue), overflowNote)
	}
	o.queue, o.size = nil, 0
}

func (o *output) write(prefix, text string) {
	fmt.Fprintf(o.w, "%s%s\n", prefix, o.held.String(text))
}

func (o *output) hold() (release func()) {
	o.mu.Lock()
	o.minting++
	o.held.pin()
	o.mu.Unlock()
	return sync.OnceFunc(func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		defer o.held.unpin()
		if o.minting--; o.minting > 0 {
			return
		}
		r, overflow := o.held.redactor()
		if overflow {
			o.dropQueue()
		}
		for _, l := range o.queue {
			fmt.Fprintf(o.w, "%s%s\n", l.prefix, r.String(l.text))
		}
		if o.dropped > 0 {
			fmt.Fprintf(o.w, "%s(%d lines of output left out)\n", o.prefix, o.dropped)
		}
		o.queue, o.size, o.dropped = nil, 0, 0
	})
}

type lineLogger struct {
	mu     sync.Mutex
	out    *output
	prefix string
	buf    []byte
	skip   bool
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
			l.out.line(l.prefix, string(l.buf[:i]))
		}
		l.skip = false
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > maxLogLine {
		if !l.skip {
			l.longLine()
		}
		l.skip = true
		l.buf = l.buf[:0]
	}
	return len(p), nil
}

func (l *lineLogger) longLine() {
	l.out.line(l.prefix, fmt.Sprintf("(a line longer than %d bytes, not logged)", maxLogLine))
}

const AccountClosedMinor = 20

const CitationsMinor = 31

const CodeExecutionsMinor = 32

const OutputMediaMinor = 33

const RateLimitHeadersMinor = 34

const GeminiEmbeddingsMinor = 36

func (e *Extension) normalizeEvent(ev llm.Event) llm.Event {
	if e == nil {
		return ev
	}
	if e.info.ProtocolMinor < CitationsMinor {
		ev.Citations = nil
		ev.Sources = nil
	}
	if e.info.ProtocolMinor < CodeExecutionsMinor {
		ev.CodeExecutions = nil
	}
	if e.info.ProtocolMinor < OutputMediaMinor {
		ev.Media = nil
		if ev.Type == llm.EventMedia && len(ev.Raw) > 0 {
			ev.Type = llm.EventNative
		}
	}
	if e.info.ProtocolMinor < CandidatesMinor {
		ev.Candidate = nil
		if ev.Type == llm.EventCandidate && len(ev.Raw) > 0 {
			ev.Type = llm.EventNative
		}
	}
	if e.info.ProtocolMinor < RateLimitHeadersMinor {
		if ev.Response != nil && len(ev.Response.Headers) > 0 {
			respCopy := *ev.Response
			respCopy.Headers = nil
			ev.Response = &respCopy
		}
		if ev.Error != nil && len(ev.Error.Headers) > 0 {
			errCopy := *ev.Error
			errCopy.Headers = nil
			ev.Error = &errCopy
		}
	}
	if ev.Type != llm.EventError || ev.Error == nil {
		return ev
	}
	if e.info.ProtocolMinor < AccountClosedMinor && ev.Error.Code == llm.CodeAccountClosed {
		errCopy := *ev.Error
		errCopy.Code = llm.CodePermission
		ev.Error = &errCopy
	}
	return ev
}
