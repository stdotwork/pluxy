package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The tests' alive clock is the real one until a test fakes it. It is set before any test runs,
// so no goroutine a test leaves behind reads the variable while it changes.
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

// fakeAliveClock stops the SDK's alive clock for one test; advance moves it on.
func fakeAliveClock(t *testing.T) (advance func(time.Duration)) {
	t.Helper()
	aliveOffset.Store(0)
	aliveFake.Store(true)
	t.Cleanup(func() { aliveFake.Store(false) })
	return func(d time.Duration) { aliveOffset.Add(int64(d)) }
}

// frameWriter records the type of each frame written to it.
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

// An alive frame goes out when upstream bytes arrive and the stream has written nothing for
// aliveAfter, counted from the call's start or its last frame; never on a call that did not ask
// for them, and never after the terminal event.
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

	// An event written restarts the count.
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

	// Nothing after the terminal event.
	if err := s.Finish(llm.Finish{Reason: llm.FinishStop}, nil); err != nil {
		t.Fatal(err)
	}
	advance(time.Hour)
	sink.arrived()
	if n := w.count(protocol.TypeAlive); n != 2 {
		t.Fatalf("%d alive frames after finish, want still 2", n)
	}

	// A stream whose call did not ask for alive frames sends none.
	w2 := &frameWriter{}
	s2 := &Stream{w: w2}
	sink.set(s2)
	advance(time.Hour)
	sink.arrived()
	if n := w2.count(protocol.TypeAlive); n != 0 {
		t.Fatalf("%d alive frames on a call that did not ask, want none", n)
	}
}

// Through Serve and sdk.Upstream: an upstream that sends bytes the handler passes nothing on
// for gets alive frames sent to core on a call with alive set, and none on one without; the
// frames arrive before the reply's terminal event.
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
