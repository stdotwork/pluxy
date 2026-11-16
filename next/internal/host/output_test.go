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

func apiKey(key string) *protocol.Credential {
	return &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}
}

// While refresh or login calls run, every output line waits until the last of them ends and is
// then logged redacted with what the calls returned; past maxHeldOutput, lines are left out with
// notes, never logged before then.
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
	first() // a second release changes nothing
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
	big := strings.Repeat("b", maxHeldOutput/2-heldLineCost) // two fill the limit exactly
	for _, l := range []string{big, big, "over sk-third-1234", "over again"} {
		o.line("[x] ", l)
	}
	note := fmt.Sprintf("[x] (more than %d bytes of output while a refresh or login runs; the rest is left out until it ends)\n", maxHeldOutput)
	if got := log.String(); got != note {
		t.Fatalf("past the limit, log = %q, want only %q", got, note)
	}
	release()
	if got, want := log.String(), note+"[x] "+big+"\n[x] "+big+"\n[x] (2 lines of output left out)\n"; got != want {
		t.Errorf("log = %.300q..., want the held lines and a count of those left out", got)
	}
}

// Each held line counts against the limit beyond its text, so empty lines can't grow the queue
// without bound.
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

// Once a line has been left out, the rest of the hold's lines are left out too, also a short one
// that would still fit: logging it would put it out of order, after the note that says the rest
// is left out.
func TestHeldOutputDropsTheRestAfterTheLimit(t *testing.T) {
	var log bytes.Buffer
	o := &output{w: &log, held: &held{}, prefix: "[x] "}
	release := o.hold()
	big := strings.Repeat("b", maxHeldOutput-heldLineCost-100) // leaves 100 of the limit
	o.line("[x] ", big)
	o.line("[x] ", strings.Repeat("c", 200)) // does not fit
	o.line("[x] ", "small")                  // would fit
	release()
	note := fmt.Sprintf("[x] (more than %d bytes of output while a refresh or login runs; the rest is left out until it ends)\n", maxHeldOutput)
	if got, want := log.String(), note+"[x] "+big+"\n[x] (2 lines of output left out)\n"; got != want {
		t.Errorf("log = %.200q...%q, want the first line and a count of the 2 left out", got, got[max(0, len(got)-100):])
	}
}

// A credential stays known while lines that may hold it are held, even when newer ones replace
// it: here a refresh's credential, printed while a long login holds the output, which two more
// refreshes of the same account replace before the login ends. The account's credential from
// before the hold, printed during it, stays known too.
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
	// Once nothing is held, only the latest two are kept, as before.
	o.line("[x] ", "sk-cred0-1234 sk-cred1-1234 sk-cred2-1234 sk-cred3-1234")
	if got, want := log.String(), "[x] using [redacted]\n[x] minted [redacted]\n[x] sk-cred0-1234 sk-cred1-1234 [redacted] [redacted]\n"; got != want {
		t.Errorf("after the hold, log = %q, want %q", got, want)
	}
}

// inProcess returns an Extension whose socket is served in this process by serve, which gets the
// call's connection and the program's stderr as core reads it, so a test can put a line in the
// output at an exact point of a call.
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

// The credential a refresh or login makes, in a line core has before the call's result, is
// logged without it. The line enters core's output path in process before the result is sent,
// so the order holds whatever the platform's pipes hold.
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

// gatedConn is a call connection that has received a whole frame: Read hands over all but its
// last byte, then waits for gate before handing that over. Close does not stop it, as closing a
// socket does not take back what has arrived.
type gatedConn struct {
	net.Conn
	data   []byte
	almost chan struct{} // closed when only the last byte is left
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

// A login closed on another goroutine while Next reads its grant keeps the output held until Next
// has recorded the grant, so a line holding it is logged without it.
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

// syncBuffer is a log both of an extension's output streams write to.
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

// waitFor waits until the log holds every line in lines: the output streams are read on their
// own, so a line can arrive after the call that wrote it has finished.
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
