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

// waitDelay bounds how long collecting an exited program waits for a child process of it that
// holds its stdout or stderr open.
const waitDelay = time.Second

// maxHandshakeLine bounds the handshake line, newline included.
const maxHandshakeLine = 4096

// Options tune how an extension is started. The zero value uses the defaults.
type Options struct {
	// Versions are the protocol versions core offers. Default: protocol.Version.
	Versions []int
	// StartTimeout bounds startup from the program running: the handshake line and describe.
	// A failed start is killed and collected before Start returns. Default 10s.
	StartTimeout time.Duration
	// StopTimeout bounds the wait for the program to exit after stdin closes. Default 5s.
	StopTimeout time.Duration
	// Log receives the program's stderr and later stdout, one prefixed line at a time, with
	// the credentials the extension holds removed (protocol.Redactor). While a refresh or login
	// runs, lines are held and written when it ends, so that the credential it makes is removed
	// from them too (output): core ends a login after 15 minutes, but a refresh has no deadline,
	// so one that hangs holds them until the program exits. At most 1 MiB is held, counting 64
	// bytes for each line besides its text; past that, the rest of the held lines are left out,
	// with notes. When more than 256 credentials are replaced while lines are held, all of the
	// held lines are left out, with notes. Default os.Stderr.
	Log io.Writer
	// ProxyURL returns core's global proxy_url setting, read at each call: the proxy of every
	// account without one of its own (proxyurl.Resolve). Nil: none.
	ProxyURL func() string
	// Timeouts returns core's global routing.timeouts setting, read at each call: the defaults
	// for every account that does not override a limit. Nil: use the protocol defaults.
	Timeouts func() *protocol.Timeouts
	// Payload returns core's payload rules, read at each generate, count_tokens and endpoint
	// call and each session turn: the call carries those that match it (protocol minor version
	// 14, 15 for a session turn). Nil: none.
	Payload func() *payload.Rules
	// SettingsSecrets returns the secret values in an account's settings, a JSON object. They
	// are removed from what every call on the account sends back and from the program's
	// output, as its credential is. Core passes configdoc.SettingsSecrets. Default: none.
	SettingsSecrets func(settings []byte) []string

	// beforeHandshakeWait, set only by tests, runs after the program has started and before
	// core waits for its handshake, to hold core up there as a stalled start does.
	beforeHandshakeWait func(deadline time.Time)
	// beforeProgramStart, set only by tests, runs just before the program is started, to hold
	// core up there as a program the system is slow to start does.
	beforeProgramStart func()
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
	out      output // the program's stderr and stdout lines

	// startTook is how long starting the program took, from core preparing its start to the
	// program running, for the error of a start that passes Options.StartTimeout.
	startTook time.Duration
}

// held are the credentials core has given the extension or got back from it, so that its output
// is logged without them: for each account the latest two (after a refresh the extension may
// still write the one it replaced), the secrets of every settings it has had (a call still
// running after changes has old ones), and what a user entered during a login. Outside a hold,
// this grows only with the accounts the extension has served and their settings changes, and goes
// with the program. A credential the extension makes (a refresh or login result) is added when the
// result arrives; output is held until then (output), and while it is, the credentials a newer one
// replaces are kept too (pin), up to maxReplaced, and dropped when the last hold ends.
type held struct {
	mu       sync.Mutex
	by       map[string][]heldCredential
	out      map[string][]heldCredential // the proxy and header secrets of each account, apart from by so no account ID can name them
	settings map[string][]heldCredential // the secrets in each account's settings, apart in the same way
	pins     int                         // output holds running
	replaced []*protocol.Credential      // those the latest two replaced while pins > 0
	overflow bool                        // more than maxReplaced were replaced during the holds running
	redact   *protocol.Redactor
}

// maxReplaced bounds the credentials kept, while output is held, because newer ones replaced them.
// Each change of a credential rebuilds the Redactor of all of them, so keeping many costs memory
// and time that grow with every renewal. The count runs as long as any hold runs, so overlapping
// holds share it, and it grows with the accounts the extension renews meanwhile: a busy extension,
// or a hold that hangs, may pass it. Past it, the replaced credentials are no longer kept and every
// held line is left out, with a note (output): lines are lost, never logged with a credential in
// clear.
const maxReplaced = 256

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

// rebuild makes the Redactor of every credential held. h.mu is held.
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

// pin keeps every credential held until unpin, also those newer ones replace: output held while
// a refresh or login runs may hold any of them, and is redacted only when the hold ends.
func (h *held) pin() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pins++
}

// unpin ends a pin; after the last, the credentials replaced meanwhile are dropped.
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

// redactor returns the Redactor of every credential held now, and whether more than maxReplaced
// were replaced during the holds running, so that some of them may be missing from it.
func (h *held) redactor() (*protocol.Redactor, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.redact, h.overflow
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
// checks the extension's description against the manifest. A start that passes
// Options.StartTimeout fails with one error (startLimitError), whether the handshake came too late
// or not at all, or describe did not finish in time.
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

// abort kills a program that failed to start and waits until it is collected.
func (e *Extension) abort() {
	e.Kill()
	<-e.exited
}

// start starts the program and waits for its handshake, within Options.StartTimeout of the
// program running, and returns that deadline, which describe keeps to too. The time the system
// takes to start the program does not count: a new program a virus scanner reads first can take
// longer than the limit to start, and the start itself cannot be stopped.
func (e *Extension) start(prog string) (time.Time, error) {
	began := time.Now()
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
		// Keep draining stdout so the program never blocks on it.
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
			// Read only once the deadline had passed: the start failed, as if the timer had won.
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
		// stdout ended without a handshake: wait for the program to exit, within the deadline.
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
			// The reader may hold part of a line it has read; only what is still in the pipe shows.
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

// startLimitError is the error of a start that passed Options.StartTimeout. what says what the
// program did not do in time, and handshake whether its handshake line could be read when core
// looked. The error also says how long starting the program took and, where the system shows
// them (Linux), the program's process state and the kernel function it waits in, read before
// the program is killed.
func (e *Extension) startLimitError(what, handshake string) error {
	details := append([]string{"starting its process took " + e.startTook.String(), "handshake: " + handshake}, processState(e.PID())...)
	return fmt.Errorf("extension %s %s within %s (%s)", e.manifest.ID, what, e.opts.StartTimeout, strings.Join(details, "; "))
}

// processState returns the state and wait channel of process pid from /proc, each left out
// where it cannot be read.
func processState(pid int) []string {
	if pid <= 0 {
		return nil
	}
	var out []string
	proc := "/proc/" + strconv.Itoa(pid) + "/"
	if b, err := os.ReadFile(proc + "stat"); err == nil {
		// The command name in parentheses may hold spaces; the state is the first field after it.
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

// describe asks the program to describe itself. A describe that times out at the start deadline
// fails with the start limit's error, not with its own timeout; any other failure keeps its own
// error.
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

// nativeOnlyMinor is the minor version that adds native-only generate calls
// (protocol.Call.NativeOnly; package nativeonly holds their rule).
const nativeOnlyMinor = 27

// ParsesNative reports whether an extension that described itself with info takes native-only
// generate calls (protocol.Call.NativeOnly): it implements minor version 27 or later and says
// it parses a request's native body itself, with the parsers core has (format.ParserVersion).
func ParsesNative(info protocol.Info) bool {
	return info.ProtocolMinor >= nativeOnlyMinor && info.NativeParser == format.ParserVersion
}

// ServesEndpoints reports whether an extension that described itself with info serves endpoint
// calls: it implements minor version 7 or later and lists the endpoint operation.
func ServesEndpoints(info protocol.Info) bool {
	return info.ProtocolMinor >= 7 && slices.Contains(info.Operations, protocol.OpEndpoint)
}

// Endpoint starts an endpoint call, answered as a generate call is (see Generate). The reply's
// body is redacted across its pieces: a secret in one of the forms core removes, or spelled with
// other escapes core decodes, that is cut between them is removed, but for the part of its start
// that a piece sent whole (redacted as one text, as when it is too heavily escaped to hold back)
// had already sent as it was; after such a piece, an escape it began that the next completes is
// replaced too, so the client does not read one there that spells a secret
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
		// The extension parses the native body itself: send the request's content once.
		sent := nativeonly.Strip(req)
		call.Request, call.NativeOnly = &sent, true
	}
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
	// the end of a piece that could be part of a secret (protocol.BodyRedactor). Its secrets
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
	// watch is how long Next waits for a frame before it ends the call with extension_timeout
	// (watchdog); 0: no limit. started is set once response.start has arrived; alives counts
	// the alive frames.
	watch   time.Duration
	started bool
	alives  atomic.Int64
	// pieces are the account's secrets, whose pieces are removed from the upstream's error text.
	pieces pieces
	// held is the call's capture, redacted, until the call ends: whether it ended in an error
	// says how much of it loses the pieces (pieces.diagnostic).
	held heldCapture
	// lost is what core left out of the request for an extension too old to take it
	// (downgradeToolResults); response.start's translation report gets it.
	lost []llm.Loss
}

// Alives returns how many alive frames the extension has sent on the call so far. It may be
// called while Next runs.
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

// Next returns the next event.
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

// last returns what Next returns once the call has ended: first the end of an endpoint reply's
// body that was held back, if any, when the body ended with finish, then the terminal event,
// again and again. A body that breaks off (an error, a cancel) drops what was held back: it was
// held only because it could be part of a secret, and the body is broken there anyway.
func (s *Stream) last() llm.Event {
	if rest := s.body.Flush(); rest != "" && s.final.Type == llm.EventFinish {
		return llm.Event{Type: llm.EventNative, Raw: bodyPiece(rest)}
	}
	return *s.final
}

// bodyPiece returns a body piece as the JSON string a native event carries. BodyRedactor cuts
// a body only where a character starts: where a form of a secret, or its spelling with escapes,
// starts or ends, at a backslash or percent sign that starts an escape, before the colon, bracket
// or comma before a number or the number, where a run of bytes that can be part of a spelling
// starts, or, after text it sent whole or replaced with [redacted], at the start of a character
// it left as it was. So the piece is valid UTF-8 as the decoded string it came from was, and
// encodes exactly.
func bodyPiece(text string) json.RawMessage {
	b, _ := json.Marshal(text)
	return b
}

// read reads the next frame, within the watchdog's limit when the call has one.
func (s *Stream) read(reply *protocol.Reply) error {
	if s.watch > 0 {
		_ = s.conn.SetReadDeadline(time.Now().Add(s.watch))
	}
	return protocol.ReadFrame(s.conn, reply)
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

// Close ends the call; if it had not finished, the extension sees it cancelled.
func (s *Stream) Close() {
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

// maxLogLine bounds a line of extension output that is logged.
const maxLogLine = 64 << 10

// maxHeldOutput bounds the extension output held while a refresh or login runs: its text, plus
// heldLineCost for each line.
const maxHeldOutput = 1 << 20

// heldLineCost is what a held line costs beyond its text (its entry in the queue, and room for
// the queue to grow), so that a flood of empty lines is bounded too.
const heldLineCost = 64

// output is where the lines of a program's stderr and stdout go: to w, each with the credentials
// the extension holds removed (held) when it is written.
//
// A refresh or login makes a credential core learns only from the call's result, and the
// extension may write it out before that result arrives, or core may read the line first. So from
// just before such a call is sent until its result is recorded (held.add), every line is held, and
// then logged redacted with it. A line can't be told apart by call, as every call shares both
// streams, so while any refresh or login runs, all lines wait, those of other calls too. A line
// read after the result is redacted with it anyway. While lines are held, held also keeps the
// credentials newer ones replace (held.pin), up to maxReplaced, so later renewals can't take one
// away before the lines it may be in are logged. Once the held lines pass maxHeldOutput, the rest
// of them are left out, with notes; once more than maxReplaced credentials have been replaced while
// they wait, all of them are left out, with notes. No held line is logged before its redaction is
// known or with it incomplete. The hold lasts as long as the call:
// core ends a login after 15 minutes, but a refresh has no deadline, so one that hangs keeps the
// lines held until the program exits, which ends it.
type output struct {
	mu      sync.Mutex
	w       io.Writer
	held    *held
	prefix  string // of the notes about left-out lines
	minting int    // refresh and login calls running
	queue   []heldLine
	size    int // what queue holds, counted as maxHeldOutput says
	dropped int // lines left out since the queue was full
}

type heldLine struct{ prefix, text string }

// line logs text with prefix, or holds it while a refresh or login runs.
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

// The notes on held lines left out.
var (
	sizeNote = fmt.Sprintf("the output held while a refresh or login runs passed its limit of %d bytes, counting %d for each line besides its text; the rest of the held output is left out until it ends",
		maxHeldOutput, heldLineCost)
	overflowNote = fmt.Sprintf("more than %d credentials were replaced while a refresh or login runs; all of the held output is left out until it ends", maxReplaced)
)

// overflowed reports whether more than maxReplaced credentials were replaced during the holds
// running, so that the held lines may hold one no longer known.
func (o *output) overflowed() bool {
	_, overflow := o.held.redactor()
	return overflow
}

// leaveOut leaves out n lines, with note the first time. o.mu is held.
func (o *output) leaveOut(n int, note string) {
	if o.dropped == 0 {
		o.note(note)
	}
	o.dropped += n
}

func (o *output) note(note string) { fmt.Fprintf(o.w, "%s(%s)\n", o.prefix, note) }

// core logs a line of core's own about the extension, at once: it holds no credential, so it
// does not wait for a refresh or login to end.
func (o *output) core(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.w, "%s%s\n", o.prefix, text)
}

// dropQueue leaves out the lines held so far, past maxReplaced, with its note: also after other
// lines were left out for the size limit, whose note says only the rest is. o.mu is held.
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

// hold holds the output from now until the returned release is called, at least once, after
// the result of the refresh or login about to be sent is recorded.
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
		// One Redactor for all the held lines: a renewal while they are written could otherwise
		// pass maxReplaced and drop a credential one of them still holds.
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

// lineLogger passes whole lines to out with a prefix. A line longer than maxLogLine is left out,
// with a note: cut into parts, a credential split between two of them would not be recognised.
type lineLogger struct {
	mu     sync.Mutex
	out    *output
	prefix string
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
			l.out.line(l.prefix, string(l.buf[:i]))
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
	l.out.line(l.prefix, fmt.Sprintf("(a line longer than %d bytes, not logged)", maxLogLine))
}

// AccountClosedMinor is the protocol minor version that adds llm.CodeAccountClosed. Core
// downgrades that code to llm.CodePermission for an extension that reports an earlier version.
const AccountClosedMinor = 20

// normalizeEvent downgrades error codes that require a newer protocol minor version than e
// reported in describe.
func (e *Extension) normalizeEvent(ev llm.Event) llm.Event {
	if e == nil || ev.Type != llm.EventError || ev.Error == nil {
		return ev
	}
	if e.info.ProtocolMinor < AccountClosedMinor && ev.Error.Code == llm.CodeAccountClosed {
		errCopy := *ev.Error
		errCopy.Code = llm.CodePermission
		ev.Error = &errCopy
	}
	return ev
}
