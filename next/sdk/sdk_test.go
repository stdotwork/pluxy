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
	if info.ProtocolMinor != protocol.Minor || !slices.Contains(info.Operations, protocol.OpSession) {
		t.Fatalf("describe: minor %d, operations %v; want minor %d with session", info.ProtocolMinor, info.Operations, protocol.Minor)
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

type blockingSession struct {
	turnEnded chan struct{}
}

func (s blockingSession) Turn(ctx context.Context, _ Call, out *Stream) error {
	if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
		return err
	}
	<-ctx.Done()
	close(s.turnEnded)
	return ctx.Err()
}
func (blockingSession) Interrupt(json.RawMessage) { select {} }
func (blockingSession) Close()                    {}

// Core closing a session's connection cancels what the extension is doing for it: an opening
// that has not finished, and a running turn while an interrupt callback blocks.
func TestSessionNoticesCoreGoing(t *testing.T) {
	openEnded := make(chan struct{})
	turnEnded := make(chan struct{})
	ext := Extension{ID: "test/sdk", Version: "0.0.0",
		Generate: func(context.Context, Call, *Stream) error { return nil },
		Session: func(ctx context.Context, call SessionCall) (Session, error) {
			if call.Account.ID == "slow" {
				<-ctx.Done()
				close(openEnded)
				return nil, ctx.Err()
			}
			return blockingSession{turnEnded: turnEnded}, nil
		}}
	socket, _, _ := startServe(t, ext)
	open := func(account string) net.Conn {
		conn := dial(t, socket)
		a := protocol.Account{ID: account}
		if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "s", Op: protocol.OpSession, Account: &a,
			Session: &protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}}); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	wait := func(c chan struct{}, what string) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not end after core closed the connection", what)
		}
	}

	conn := open("slow")
	time.Sleep(100 * time.Millisecond)
	conn.Close()
	wait(openEnded, "the opening")

	conn = open("a")
	defer conn.Close()
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil || r.Type != protocol.TypeSessionReady {
		t.Fatalf("session reply %+v, %v", r, err)
	}
	a := protocol.Account{ID: "a"}
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn, Account: &a, Request: &llm.Request{Model: "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ReadFrame(conn, &r); err != nil || r.Type != llm.EventResponseStart {
		t.Fatalf("turn's first event %+v, %v", r, err)
	}
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionInterrupt, Raw: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	conn.Close()
	wait(turnEnded, "the turn")
}

// An extension with a CountTokens handler lists count_tokens in describe and answers count calls
// with it; one without answers them unsupported.
func TestCountTokens(t *testing.T) {
	count := func(_ context.Context, call Call, out *Stream) error {
		if err := out.Start(llm.ResponseInfo{Model: call.Request.Model, Format: llm.FormatAnthropicMessages}); err != nil {
			return err
		}
		return out.Finish(llm.Finish{Reason: llm.FinishStop}, &llm.Usage{InputTokens: llm.Int64(42), Raw: json.RawMessage(`{"input_tokens":42}`)})
	}
	generate := func(context.Context, Call, *Stream) error { return fmt.Errorf("generate called") }
	read := func(t *testing.T, conn net.Conn) []llm.Event {
		t.Helper()
		var evs []llm.Event
		for {
			var r protocol.Reply
			if err := protocol.ReadFrame(conn, &r); err != nil {
				t.Fatalf("after %v: %v", evs, err)
			}
			evs = append(evs, r.Event)
			if r.Event.Terminal() {
				return evs
			}
		}
	}
	for _, tc := range []struct {
		name  string
		count Handler
	}{{"counts", count}, {"cannot count", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			socket, stdin, _ := startServe(t, Extension{ID: "test/sdk", Version: "0.0.0", Generate: generate, CountTokens: tc.count})
			defer stdin.Close()
			conn := dial(t, socket)
			_ = protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "c-1", Op: protocol.OpDescribe})
			var described protocol.Reply
			if err := protocol.ReadFrame(conn, &described); err != nil || described.Info == nil {
				t.Fatalf("describe: %+v, %v", described, err)
			}
			conn.Close()
			if listed := slices.Contains(described.Info.Operations, protocol.OpCountTokens); listed != (tc.count != nil) || described.Info.ProtocolMinor < 2 {
				t.Errorf("describe: minor %d, operations %v", described.Info.ProtocolMinor, described.Info.Operations)
			}
			conn = dial(t, socket)
			defer conn.Close()
			_ = protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "c-2", Op: protocol.OpCountTokens, Request: &llm.Request{Model: "m"}})
			evs := read(t, conn)
			last := evs[len(evs)-1]
			if tc.count == nil {
				if last.Type != llm.EventError || last.Error.Code != llm.CodeUnsupported {
					t.Errorf("events = %+v, want unsupported", evs)
				}
				return
			}
			if len(evs) != 2 || last.Type != llm.EventFinish || last.Usage == nil || *last.Usage.InputTokens != 42 {
				t.Errorf("events = %+v, want start and a finish with the count", evs)
			}
		})
	}
}

// TestModels covers the models operation: describe lists it, a call returns the handler's list
// with the account it was given, a nil list is an empty one, and a panic is an internal error.
func TestModels(t *testing.T) {
	ext := Extension{ID: "test/models", Version: "0.0.0", Generate: func(context.Context, Call, *Stream) error { return nil },
		Models: func(_ context.Context, call ModelsCall) (*protocol.ModelList, error) {
			switch call.Account.ID {
			case "none":
				return nil, nil
			case "panic":
				panic("boom")
			}
			return &protocol.ModelList{OwnedBy: "test", Models: []protocol.Model{{ID: call.Account.ID + "-model", ContextLength: 8}}}, nil
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
	if !slices.Contains(described.Info.Operations, protocol.OpModels) || described.Info.ProtocolMinor < 3 {
		t.Fatalf("describe: %+v; want models with minor 3 or more", described.Info)
	}

	call := func(id string) protocol.Reply {
		t.Helper()
		conn := dial(t, socket)
		defer conn.Close()
		if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "m", Op: protocol.OpModels, Account: &protocol.Account{ID: id}}); err != nil {
			t.Fatal(err)
		}
		var r protocol.Reply
		if err := protocol.ReadFrame(conn, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call("a"); r.Type != protocol.TypeModelsResult || r.Models == nil || r.Models.OwnedBy != "test" ||
		len(r.Models.Models) != 1 || r.Models.Models[0].ID != "a-model" || r.Models.Models[0].ContextLength != 8 {
		t.Errorf("models of a: %+v %+v", r.Event, r.Models)
	}
	if r := call("none"); r.Type != protocol.TypeModelsResult || r.Models == nil || r.Models.Models == nil || len(r.Models.Models) != 0 {
		t.Errorf("models of none: %+v %+v; want an empty list", r.Event, r.Models)
	}
	if r := call("panic"); r.Type != llm.EventError || r.Error == nil || r.Error.Code != llm.CodeInternal {
		t.Errorf("models of panic: %+v; want an internal error", r.Event)
	}
}
