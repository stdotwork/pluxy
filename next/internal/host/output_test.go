package host

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

var wantSizeNote = fmt.Sprintf("[x] (the output held while a refresh or login runs passed its limit of %d bytes, counting %d for each line besides its text; the rest of the held output is left out until it ends)\n", maxHeldOutput, heldLineCost)
var wantOverflowNote = fmt.Sprintf("[x] (more than %d credentials were replaced while a refresh or login runs; all of the held output is left out until it ends)\n", maxReplaced)

func apiKey(key string) *protocol.Credential {
	return &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}
}

func TestOutputIsHeldWhileACredentialIsMade(t *testing.T) {
	var log bytes.Buffer
	h := &held{}
	o := &output{w: &log, held: h, prefix: "[x] "}
	o.line("[x] ", "before")
	first, second := o.hold(), o.hold()
	o.line("[x] ", "first made sk-first-1234")
	o.line("[x] stdout] ", "no call's line")
	h.add("a", apiKey("sk-first-1234"))
	first()
	first()
	o.line("[x] ", "second made sk-second-1234")
	if got, want := log.String(), "[x] before\n"; got != want {
		t.Fatalf("while a call still runs, log = %q, want %q", got, want)
	}
	h.add("b", apiKey("sk-second-1234"))
	second()
	o.line("[x] ", "after")
	want := "[x] before\n[x] first made [redacted]\n[x] stdout] no call's line\n[x] second made [redacted]\n[x] after\n"
	if got := log.String(); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}

	log.Reset()
	release := o.hold()
	big := strings.Repeat("b", maxHeldOutput/2-heldLineCost)
	for _, l := range []string{big, big, "over sk-third-1234", "over again"} {
		o.line("[x] ", l)
	}
	note := wantSizeNote
	if got := log.String(); got != note {
		t.Fatalf("past the limit, log = %q, want only %q", got, note)
	}
	release()
	if got, want := log.String(), note+"[x] "+big+"\n[x] "+big+"\n[x] (2 lines of output left out)\n"; got != want {
		t.Errorf("log = %.300q..., want the held lines and a count of those left out", got)
	}
}

func TestHeldEmptyLinesAreBounded(t *testing.T) {
	var log bytes.Buffer
	o := &output{w: &log, held: &held{}, prefix: "[x] "}
	release := o.hold()
	fits := maxHeldOutput / heldLineCost
	for range fits + 5 {
		o.line("[x] ", "")
	}
	if n := len(o.queue); n != fits {
		t.Errorf("%d empty lines held, want at most %d", n, fits)
	}
	release()
	if got := strings.Count(log.String(), "[x] \n"); got != fits {
		t.Errorf("%d empty lines logged, want %d", got, fits)
	}
	if !strings.HasSuffix(log.String(), "[x] (5 lines of output left out)\n") {
		t.Errorf("log ends %q, want a count of the 5 lines left out", log.String()[max(0, log.Len()-100):])
	}
}

func TestHeldOutputDropsTheRestAfterTheLimit(t *testing.T) {
	var log bytes.Buffer
	o := &output{w: &log, held: &held{}, prefix: "[x] "}
	release := o.hold()
	big := strings.Repeat("b", maxHeldOutput-heldLineCost-100)
	o.line("[x] ", big)
	o.line("[x] ", strings.Repeat("c", 200))
	o.line("[x] ", "small")
	release()
	note := wantSizeNote
	if got, want := log.String(), note+"[x] "+big+"\n[x] (2 lines of output left out)\n"; got != want {
		t.Errorf("log = %.200q...%q, want the first line and a count of the 2 left out", got, got[max(0, len(got)-100):])
	}
}

func TestHeldOutputKeepsReplacedCredentials(t *testing.T) {
	var log bytes.Buffer
	h := &held{}
	o := &output{w: &log, held: h, prefix: "[x] "}
	h.add("a", apiKey("sk-cred0-1234"))
	login := o.hold()
	o.line("[x] ", "using sk-cred0-1234")
	for i, key := range []string{"sk-cred1-1234", "sk-cred2-1234", "sk-cred3-1234"} {
		refresh := o.hold()
		if i == 0 {
			o.line("[x] ", "minted "+key)
		}
		h.add("a", apiKey(key))
		refresh()
	}
	login()
	if got, want := log.String(), "[x] using [redacted]\n[x] minted [redacted]\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
	o.line("[x] ", "sk-cred0-1234 sk-cred1-1234 sk-cred2-1234 sk-cred3-1234")
	if got, want := log.String(), "[x] using [redacted]\n[x] minted [redacted]\n[x] sk-cred0-1234 sk-cred1-1234 [redacted] [redacted]\n"; got != want {
		t.Errorf("after the hold, log = %q, want %q", got, want)
	}
}

func TestHeldOutputPastMaxReplacedIsLeftOut(t *testing.T) {
	var log bytes.Buffer
	h := &held{}
	o := &output{w: &log, held: h, prefix: "[x] "}
	release := o.hold()
	renew := renewer(h)
	renew(0, 20+maxReplaced)
	if n := len(h.replaced); n != maxReplaced || h.overflow {
		t.Fatalf("%d replaced kept (overflow %v), want %d kept", n, h.overflow, maxReplaced)
	}
	o.line("[x] ", "minted "+renewedKey(0))
	renew(20+maxReplaced, 21+maxReplaced)
	if n := len(h.replaced); n != 0 || !h.overflow {
		t.Fatalf("past the limit, %d replaced kept (overflow %v), want none and overflow", n, h.overflow)
	}
	o.line("[x] ", "later "+renewedKey(1))
	release()
	if got, want := log.String(), wantOverflowNote+"[x] (2 lines of output left out)\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
	if h.overflow || h.replaced != nil {
		t.Errorf("after the hold, overflow %v and %d replaced kept, want neither", h.overflow, len(h.replaced))
	}
}

func renewedKey(i int) string { return fmt.Sprintf("sk-renewed-%04d", i) }

func renewer(h *held) func(from, to int) {
	return func(from, to int) {
		for i := from; i < to; i++ {
			h.add(fmt.Sprintf("account %d", i%10), apiKey(renewedKey(i)))
		}
	}
}

func TestHeldLinesPastMaxReplacedAreLeftOutAtRelease(t *testing.T) {
	var log bytes.Buffer
	h := &held{}
	o := &output{w: &log, held: h, prefix: "[x] "}
	release := o.hold()
	renew := renewer(h)
	renew(0, 20)
	o.line("[x] ", "minted "+renewedKey(0))
	renew(20, 21+maxReplaced)
	release()
	if got, want := log.String(), wantOverflowNote+"[x] (1 lines of output left out)\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestHeldLinesPastBothLimitsNameTheCap(t *testing.T) {
	var log bytes.Buffer
	h := &held{}
	o := &output{w: &log, held: h, prefix: "[x] "}
	release := o.hold()
	o.line("[x] ", strings.Repeat("b", maxHeldOutput-heldLineCost-10))
	o.line("[x] ", "over the size limit")
	renewer(h)(0, 21+maxReplaced)
	release()
	if got, want := log.String(), wantSizeNote+wantOverflowNote+"[x] (2 lines of output left out)\n"; got != want {
		t.Errorf("log = %.400q, want %q", got, want)
	}
}

type renewingWriter struct {
	bytes.Buffer
	renew func()
}

func (w *renewingWriter) Write(p []byte) (int, error) {
	if w.renew != nil {
		w.renew()
		w.renew = nil
	}
	return w.Buffer.Write(p)
}

func TestHeldLinesAreRedactedWithOneRedactor(t *testing.T) {
	h := &held{}
	renew := renewer(h)
	passed := false
	log := &renewingWriter{renew: func() {
		renew(20+maxReplaced, 21+maxReplaced)
		passed = h.overflow
	}}
	o := &output{w: log, held: h, prefix: "[x] "}
	release := o.hold()
	renew(0, 20+maxReplaced)
	o.line("[x] ", "first")
	o.line("[x] ", "minted "+renewedKey(0))
	release()
	if !passed {
		t.Fatal("the renewal while the lines were written did not pass maxReplaced")
	}
	if got, want := log.String(), "[x] first\n[x] minted [redacted]\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func inProcess(t *testing.T, log io.Writer, serve func(conn net.Conn, stderr io.Writer)) *Extension {
	t.Helper()
	dir, err := os.MkdirTemp("", "pluxy-out-") // short: a socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := &Extension{manifest: &Manifest{ID: "test/x"}, socket: filepath.Join(dir, "s"), exited: make(chan struct{})}
	e.out = output{w: log, held: &e.held, prefix: "[test/x] "}
	ln, err := net.Listen("unix", e.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serve(conn, &lineLogger{out: &e.out, prefix: "[test/x] "})
	}()
	return e
}

func TestFreshCredentialInOutputBeforeItsResult(t *testing.T) {
	for _, op := range []string{"refresh", "login"} {
		t.Run(op, func(t *testing.T) {
			const token = "fresh-in-process-1234"
			log := &syncBuffer{}
			c := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: token}
			e := inProcess(t, log, func(conn net.Conn, stderr io.Writer) {
				var call protocol.Call
				if protocol.ReadFrame(conn, &call) != nil {
					return
				}
				fmt.Fprintf(stderr, "minted %s\n", token)
				done := protocol.TypeRefreshDone
				if call.Op == protocol.OpLogin {
					done = protocol.TypeLoginDone
				}
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: done}, Grant: &protocol.Grant{Credential: c}})
			})
			var got *protocol.Credential
			if op == "refresh" {
				var e2 *llm.Error
				got, e2 = e.Refresh(context.Background(), protocol.Account{ID: "a", Credential: apiKey("sk-old-1234")})
				if e2 != nil {
					t.Fatal(e2)
				}
			} else {
				l := e.Login(context.Background(), protocol.Account{ID: "a"}, protocol.Login{Method: "m"})
				r := l.Next()
				l.Close()
				if r.Grant == nil {
					t.Fatalf("login = %+v, want a grant", r)
				}
				got = r.Grant.Credential
			}
			if got == nil || got.AccessToken != token {
				t.Fatalf("%s returned %+v, want the minted credential", op, got)
			}
			if out, want := log.String(), "[test/x] minted [redacted]\n"; out != want {
				t.Errorf("log = %q, want %q", out, want)
			}
		})
	}
}

type gatedConn struct {
	net.Conn
	data   []byte
	almost chan struct{}
	gate   chan struct{}
}

func (c *gatedConn) Read(p []byte) (int, error) {
	switch len(c.data) {
	case 0:
		return 0, io.EOF
	case 1:
		close(c.almost)
		<-c.gate
	default:
		p = p[:min(len(p), len(c.data)-1)]
	}
	n := copy(p, c.data)
	c.data = c.data[n:]
	return n, nil
}

func (c *gatedConn) Close() error { return nil }

func TestLoginClosedWhileNextReadsItsGrant(t *testing.T) {
	const token = "fresh-close-race-1234"
	log := &syncBuffer{}
	e := &Extension{manifest: &Manifest{ID: "test/x"}}
	e.out = output{w: log, held: &e.held, prefix: "[test/x] "}
	var frame bytes.Buffer
	if err := protocol.WriteFrame(&frame, protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginDone},
		Grant: &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: token}}}); err != nil {
		t.Fatal(err)
	}
	conn := &gatedConn{data: frame.Bytes(), almost: make(chan struct{}), gate: make(chan struct{})}
	l := &LoginCall{ext: e, ctx: context.Background(), account: "a", conn: conn, release: e.out.hold()}
	next := make(chan protocol.Reply)
	go func() { next <- l.Next() }()
	<-conn.almost
	e.out.line("[test/x] ", "minted "+token)
	l.Close()
	close(conn.gate)
	if r := <-next; r.Grant == nil {
		t.Fatalf("login = %+v, want a grant", r)
	}
	if out, want := log.String(), "[test/x] minted [redacted]\n"; out != want {
		t.Errorf("log = %q, want %q", out, want)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) waitFor(t *testing.T, lines ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, missing := b.String(), ""
		for _, l := range lines {
			if !strings.Contains(out, l) {
				missing = l
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never got %q:\n%s", missing, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
