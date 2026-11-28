package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

var (
	aliveFake   atomic.Bool
	aliveOffset atomic.Int64
)

func init() {
	start := time.Unix(1_700_000_000, 0)
	aliveClock = func() time.Time {
		if aliveFake.Load() {
			return start.Add(time.Duration(aliveOffset.Load()))
		}
		return time.Now()
	}
}

func fakeAliveClock(t *testing.T) (advance func(time.Duration)) {
	t.Helper()
	aliveOffset.Store(0)
	aliveFake.Store(true)
	t.Cleanup(func() { aliveFake.Store(false) })
	return func(d time.Duration) { aliveOffset.Add(int64(d)) }
}

type frameWriter struct {
	mu    sync.Mutex
	types []string
}

func (w *frameWriter) Write(p []byte) (int, error) {
	var r protocol.Reply
	if err := json.Unmarshal(p[4:], &r); err != nil {
		return 0, err
	}
	w.mu.Lock()
	w.types = append(w.types, r.Type)
	w.mu.Unlock()
	return len(p), nil
}

func (w *frameWriter) count(typ string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, t := range w.types {
		if t == typ {
			n++
		}
	}
	return n
}

func TestAliveTimer(t *testing.T) {
	advance := fakeAliveClock(t)
	w := &frameWriter{}
	s := &Stream{w: w}
	s.startAlive()
	sink := &aliveSink{}
	sink.set(s)

	advance(aliveAfter - time.Second)
	sink.arrived()
	if n := w.count(protocol.TypeAlive); n != 0 {
		t.Fatalf("%d alive frames %s after the start, want none", n, aliveAfter-time.Second)
	}
	advance(time.Second)
	sink.arrived()
	if n := w.count(protocol.TypeAlive); n != 1 {
		t.Fatalf("%d alive frames %s after the start, want 1", n, aliveAfter)
	}
	advance(aliveAfter / 2)
	sink.arrived()
	if n := w.count(protocol.TypeAlive); n != 1 {
		t.Fatalf("%d alive frames half the interval after the last, want still 1", n)
	}

	if err := s.Start(llm.ResponseInfo{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	advance(aliveAfter - time.Millisecond)
	sink.arrived()
	if n := w.count(protocol.TypeAlive); n != 1 {
		t.Fatalf("%d alive frames just under the interval after an event, want still 1", n)
	}
	advance(time.Millisecond)
	sink.arrived()
	if n := w.count(protocol.TypeAlive); n != 2 {
		t.Fatalf("%d alive frames the interval after an event, want 2", n)
	}

	if err := s.Finish(llm.Finish{Reason: llm.FinishStop}, nil); err != nil {
		t.Fatal(err)
	}
	advance(time.Hour)
	sink.arrived()
	if n := w.count(protocol.TypeAlive); n != 2 {
		t.Fatalf("%d alive frames after finish, want still 2", n)
	}

	w2 := &frameWriter{}
	s2 := &Stream{w: w2}
	sink.set(s2)
	advance(time.Hour)
	sink.arrived()
	if n := w2.count(protocol.TypeAlive); n != 0 {
		t.Fatalf("%d alive frames on a call that did not ask, want none", n)
	}
}

func TestAliveFromUpstreamBytes(t *testing.T) {
	advance := fakeAliveClock(t)
	next := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for i := range 3 {
			select {
			case <-next:
			case <-r.Context().Done():
				return
			}
			fmt.Fprintf(w, ": ping %d\n\n", i)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(up.Close)
	client := &http.Client{Transport: Upstream(&http.Transport{})}

	socket, _, _ := startServe(t, Extension{ID: "t", Version: "0", Generate: func(ctx context.Context, call Call, out *Stream) error {
		if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
			return err
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, up.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		buf := make([]byte, 64)
		for {
			if _, err := resp.Body.Read(buf); err == io.EOF {
				break
			} else if err != nil {
				return err
			}
		}
		return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
	}})

	run := func(alive bool) []string {
		conn := dial(t, socket)
		defer conn.Close()
		if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "1", Op: protocol.OpGenerate,
			Account: &protocol.Account{ID: "a"}, Request: &llm.Request{Model: "m", Stream: true}, Alive: alive}); err != nil {
			t.Fatal(err)
		}
		var types []string
		read := func() string {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			var r protocol.Reply
			if err := protocol.ReadFrame(conn, &r); err != nil {
				t.Fatalf("reading a frame: %v (so far %v)", err, types)
			}
			types = append(types, r.Type)
			return r.Type
		}
		if typ := read(); typ != llm.EventResponseStart {
			t.Fatalf("first frame %q, want response.start", typ)
		}
		for range 3 {
			advance(aliveAfter)
			next <- struct{}{}
			if alive {
				if typ := read(); typ != protocol.TypeAlive {
					t.Fatalf("frame after upstream bytes %q, want alive (so far %v)", typ, types)
				}
			}
		}
		if typ := read(); typ != llm.EventFinish {
			t.Fatalf("last frame %q, want finish (so far %v)", typ, types)
		}
		return types
	}
	if got := run(true); len(got) != 5 {
		t.Errorf("frames with alive = %v, want start, three alive, finish", got)
	}
	if got := run(false); len(got) != 2 {
		t.Errorf("frames without alive = %v, want start and finish only", got)
	}
}

func TestAliveLeavesNoBodyAlone(t *testing.T) {
	advance := fakeAliveClock(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(up.Close)
	client := &http.Client{Transport: Upstream(&http.Transport{})}
	sameBody := make(chan bool, 1)
	socket, _, _ := startServe(t, Extension{ID: "t", Version: "0", Generate: func(ctx context.Context, call Call, out *Stream) error {
		if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
			return err
		}
		advance(aliveAfter)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, up.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		sameBody <- resp.Body == http.NoBody
		return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
	}})
	conn := dial(t, socket)
	defer conn.Close()
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "1", Op: protocol.OpGenerate,
		Account: &protocol.Account{ID: "a"}, Request: &llm.Request{Model: "m", Stream: true}, Alive: true}); err != nil {
		t.Fatal(err)
	}
	var types []string
	for len(types) == 0 || types[len(types)-1] != llm.EventFinish {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var r protocol.Reply
		if err := protocol.ReadFrame(conn, &r); err != nil {
			t.Fatalf("reading a frame: %v (so far %v)", err, types)
		}
		types = append(types, r.Type)
	}
	if !<-sameBody {
		t.Error("the response body is not http.NoBody: the alive layer wrapped it")
	}
	if len(types) != 3 || types[1] != protocol.TypeAlive {
		t.Errorf("frames = %v, want start, alive (the headers arrived), finish", types)
	}
}

func TestAliveOnSessionTurns(t *testing.T) {
	advance := fakeAliveClock(t)
	conns := make(chan chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: test\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		next := make(chan struct{})
		conns <- next
		gone := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, rw)
			close(gone)
		}()
		for {
			select {
			case <-next:
			case <-gone:
				return
			}
			if _, err := rw.WriteString("x"); err != nil || rw.Flush() != nil {
				return
			}
		}
	}))
	t.Cleanup(up.Close)
	client := &http.Client{Transport: Upstream(&http.Transport{})}

	ext := Extension{ID: "t", Version: "0", Generate: func(context.Context, Call, *Stream) error { return nil },
		Session: func(ctx context.Context, _ SessionCall) (Session, error) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, up.URL, nil)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "test")
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			if _, ok := resp.Body.(io.ReadWriteCloser); !ok || resp.StatusCode != http.StatusSwitchingProtocols {
				return nil, fmt.Errorf("status %d, body %T: not an upgraded connection", resp.StatusCode, resp.Body)
			}
			var next chan struct{}
			select {
			case next = <-conns:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &upgradedSession{body: resp.Body, turn: func(_ context.Context, _ Call, out *Stream) error {
				if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
					return err
				}
				advance(aliveAfter)
				next <- struct{}{}
				var b [1]byte
				if _, err := io.ReadFull(resp.Body, b[:]); err != nil {
					return err
				}
				return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
			}}, nil
		}}
	socket, _, _ := startServe(t, ext)

	for _, alive := range []bool{true, false} {
		conn := dial(t, socket)
		account := protocol.Account{ID: "a"}
		if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "s", Op: protocol.OpSession, Account: &account,
			Session: &protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}, Alive: alive}); err != nil {
			t.Fatal(err)
		}
		read := func() protocol.Reply {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			var r protocol.Reply
			if err := protocol.ReadFrame(conn, &r); err != nil {
				t.Fatalf("alive=%t: reading a frame: %v", alive, err)
			}
			return r
		}
		if r := read(); r.Type != protocol.TypeSessionReady {
			t.Fatalf("alive=%t: first frame %q, want session.ready (%v)", alive, r.Type, r.Event.Error)
		}
		if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn, Account: &account, Alive: alive, Request: &llm.Request{Model: "m"}}); err != nil {
			t.Fatal(err)
		}
		var types []string
		for len(types) == 0 || types[len(types)-1] != llm.EventFinish {
			types = append(types, read().Type)
		}
		want := []string{llm.EventResponseStart, llm.EventFinish}
		if alive {
			want = []string{llm.EventResponseStart, protocol.TypeAlive, llm.EventFinish}
		}
		if !slices.Equal(types, want) {
			t.Errorf("alive=%t: frames = %v, want %v", alive, types, want)
		}
		conn.Close()
	}
}

type upgradedSession struct {
	body io.Closer
	turn func(context.Context, Call, *Stream) error
}

func (s *upgradedSession) Turn(ctx context.Context, c Call, out *Stream) error {
	return s.turn(ctx, c, out)
}
func (*upgradedSession) Interrupt(json.RawMessage) {}
func (s *upgradedSession) Close()                  { s.body.Close() }
