package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestShutdownClosesEveryConnection closes stdin while connections are open in states that do
// not end by themselves, and checks Serve still returns.
func TestShutdownClosesEveryConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		// open opens a connection to the extension and leaves it in the state under test.
		open func(t *testing.T, socket string) net.Conn
	}{
		{"awaiting its first frame", func(t *testing.T, socket string) net.Conn {
			return dial(t, socket)
		}},
		{"call blocked writing to core", func(t *testing.T, socket string) net.Conn {
			conn := dial(t, socket)
			call := protocol.Call{Type: protocol.TypeCall, ID: "c-1", Op: protocol.OpGenerate, Request: &llm.Request{Model: "m"}}
			if err := protocol.WriteFrame(conn, call); err != nil {
				t.Fatal(err)
			}
			// Core reads nothing, so the handler's writes fill the socket and block.
			return conn
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wroteAll := make(chan struct{})
			ext := Extension{ID: "test/sdk", Version: "0.0.0", Generate: func(ctx context.Context, call Call, out *Stream) error {
				if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
					return err
				}
				i, err := out.StartItem(llm.ItemStart{Kind: llm.ItemMessage})
				if err != nil {
					return err
				}
				chunk := strings.Repeat("x", 64<<10)
				for n := 0; ; n++ {
					if n == 64 { // 4 MiB is far beyond any socket buffer, so the writes are not blocking
						close(wroteAll)
					}
					if err := out.Text(i, chunk); err != nil {
						return err
					}
				}
			}}
			socket, stdin, done := startServe(t, ext)
			conn := tc.open(t, socket)
			defer conn.Close()
			if tc.name == "call blocked writing to core" {
				select {
				case <-wroteAll:
					t.Fatal("the handler wrote 4 MiB to a reader that reads nothing")
				case <-time.After(500 * time.Millisecond):
				}
			} else {
				time.Sleep(100 * time.Millisecond) // let Serve accept the connection
			}
			stdin.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Serve returned %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Serve had not returned 10s after stdin closed")
			}
		})
	}
}

// startServe runs serve on a fresh socket and waits for its handshake. Closing the returned
// writer closes serve's stdin; done receives serve's result.
func startServe(t *testing.T, ext Extension) (socket string, stdin io.WriteCloser, done <-chan error) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pluxy-sdk-") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket = filepath.Join(dir, "s")
	t.Setenv(protocol.EnvProtocolVersions, "0")
	t.Setenv(protocol.EnvSocket, socket)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- serve(ext, inR, outW) }()
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if _, _, addr, err := protocol.ParseHandshake(line); err != nil || addr != socket {
		t.Fatalf("handshake %q: %v", line, err)
	}
	t.Cleanup(func() { inW.Close() })
	return socket, inW, errc
}

func dial(t *testing.T, socket string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

type testSession struct {
	turn func(ctx context.Context, call Call, out *Stream) error
}

func (s testSession) Turn(ctx context.Context, call Call, out *Stream) error {
	return s.turn(ctx, call, out)
}
func (testSession) Interrupt(json.RawMessage) {}
func (testSession) Close()                    {}

// An extension with a session handler describes itself with protocol minor version 1 and the
// session operation. A turn the handler leaves unfinished, or that panics, ends with an error
// event, and the session takes the next turn.
func TestSessionTurns(t *testing.T) {
	n := 0
	ext := Extension{ID: "test/sdk", Version: "0.0.0",
		Generate: func(context.Context, Call, *Stream) error { return nil },
		Session: func(_ context.Context, call SessionCall) (Session, error) {
			return testSession{turn: func(_ context.Context, call Call, out *Stream) error {
				n++
				switch call.Request.Model {
				case "unfinished":
					return out.Start(llm.ResponseInfo{ID: "r"})
				case "panic":
					panic("boom")
				}
				if err := out.Start(llm.ResponseInfo{ID: fmt.Sprintf("turn-%d", n)}); err != nil {
					return err
				}
				return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
			}}, nil
		}}
	socket, _, _ := startServe(t, ext)

	conn := dial(t, socket)
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "d", Op: protocol.OpDescribe}); err != nil {
		t.Fatal(err)
	}
	var described protocol.Reply
	if err := protocol.ReadFrame(conn, &described); err != nil || described.Info == nil {
		t.Fatalf("describe: %+v, %v", described, err)
	}
	conn.Close()
	info := described.Info
	if info.ProtocolMinor != 1 || !slices.Contains(info.Operations, protocol.OpSession) {
		t.Fatalf("describe: minor %d, operations %v; want minor 1 with session", info.ProtocolMinor, info.Operations)
	}

	conn = dial(t, socket)
	defer conn.Close()
	account := protocol.Account{ID: "a"}
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "s", Op: protocol.OpSession, Account: &account,
		Session: &protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}}); err != nil {
		t.Fatal(err)
	}
	var ready protocol.Reply
	if err := protocol.ReadFrame(conn, &ready); err != nil || ready.Type != protocol.TypeSessionReady {
		t.Fatalf("session reply %+v, %v; want session.ready", ready, err)
	}
	for _, c := range []struct{ model, last string }{{"unfinished", llm.EventError}, {"panic", llm.EventError}, {"m", llm.EventFinish}} {
		if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn, Account: &account, Request: &llm.Request{Model: c.model}}); err != nil {
			t.Fatal(err)
		}
		for {
			var r protocol.Reply
			if err := protocol.ReadFrame(conn, &r); err != nil {
				t.Fatalf("turn %s: %v", c.model, err)
			}
			if r.Event.Terminal() {
				if r.Type != c.last {
					t.Errorf("turn %s ended with %+v, want %s", c.model, r.Event, c.last)
				}
				break
			}
		}
	}
}
