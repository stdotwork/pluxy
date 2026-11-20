package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func shortKeepAlive(t *testing.T, d time.Duration) {
	t.Helper()
	old := keepAliveAfter
	keepAliveAfter = d
	t.Cleanup(func() { keepAliveAfter = old })
}

// A quiet stream gets a keep-alive comment each interval; a write puts the next one off; none
// goes in the middle of an event; none after stop.
func TestKeepAliveComments(t *testing.T) {
	shortKeepAlive(t, 50*time.Millisecond)
	rec := httptest.NewRecorder()
	k := startKeepAlive(rec, http.NewResponseController(rec))
	body := func() string {
		k.mu.Lock()
		defer k.mu.Unlock()
		return rec.Body.String()
	}
	time.Sleep(130 * time.Millisecond)
	if n := strings.Count(body(), keepAliveComment); n < 1 || n > 3 {
		t.Fatalf("%d comments after 130ms of quiet at 50ms, want 1 to 3: %q", n, body())
	}
	k.mu.Lock()
	flushed := rec.Flushed
	k.mu.Unlock()
	if !flushed {
		t.Error("the comment was not flushed")
	}

	// Half an event, then quiet: no comment may split it.
	if _, err := k.Write([]byte("event: x\r\ndata: {\"a\"")); err != nil {
		t.Fatal(err)
	}
	before := strings.Count(body(), keepAliveComment)
	time.Sleep(130 * time.Millisecond)
	if n := strings.Count(body(), keepAliveComment); n != before {
		t.Fatalf("a comment went inside an event: %q", body())
	}
	// The event ends (with CRLF line ends); comments come again.
	if _, err := k.Write([]byte("}\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(130 * time.Millisecond)
	got := body()
	if n := strings.Count(got, keepAliveComment); n == before {
		t.Fatalf("no comment after the event ended: %q", got)
	}
	if !strings.Contains(got, "data: {\"a\"}\r\n\r\n"+keepAliveComment) {
		t.Errorf("the event and the comment after it are not whole: %q", got)
	}

	k.stop()
	after := body()
	time.Sleep(130 * time.Millisecond)
	if body() != after {
		t.Errorf("a comment after stop: %q", body())
	}
}

// Writes more often than the interval keep the comments away. The writes span three intervals,
// so a write that did not put the comment off would let one through.
func TestKeepAliveNotWhileWriting(t *testing.T) {
	shortKeepAlive(t, 200*time.Millisecond)
	rec := httptest.NewRecorder()
	k := startKeepAlive(rec, http.NewResponseController(rec))
	for range 30 {
		time.Sleep(20 * time.Millisecond)
		if _, err := k.Write([]byte("data: {}\n\n")); err != nil {
			t.Fatal(err)
		}
	}
	k.stop()
	if strings.Contains(rec.Body.String(), keepAliveComment) {
		t.Errorf("a comment between writes 20ms apart, with 200ms before one: %q", rec.Body.String())
	}
}

// blockingWriter stops the first write until the test releases it, so a timer callback can queue
// behind the keep-alive mutex while that write is still in progress.
type blockingWriter struct {
	mu      sync.Mutex
	body    strings.Builder
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *blockingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

// A timer callback that fired while a write held the lock must not comment as soon as that write
// ends: the write put the next comment off by a full interval.
func TestKeepAliveStaleFireWaits(t *testing.T) {
	shortKeepAlive(t, 200*time.Millisecond)
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	rec := httptest.NewRecorder()
	k := startKeepAlive(w, http.NewResponseController(rec))
	written := make(chan error, 1)
	go func() {
		_, err := k.Write([]byte("data: one\n\n"))
		written <- err
	}()
	<-w.entered

	// The timer fires while Write holds k.mu and waits there.
	time.Sleep(2 * keepAliveAfter)
	close(w.release)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	time.Sleep(keepAliveAfter / 4)
	if got := w.String(); strings.Contains(got, keepAliveComment) {
		t.Fatalf("a stale timer callback wrote a comment right after a write: %q", got)
	}
	k.stop()
}

// A terminal event stops future comments, including one whose timer was already due.
func TestKeepAliveStopsAtFinish(t *testing.T) {
	shortKeepAlive(t, 40*time.Millisecond)
	rec := httptest.NewRecorder()
	k := startKeepAlive(rec, http.NewResponseController(rec))
	k.mu.Lock()
	k.lastWrite = time.Now().Add(-keepAliveAfter)
	k.mu.Unlock()
	k.fire()
	after := rec.Body.String()
	if !strings.Contains(after, keepAliveComment) {
		t.Fatalf("no comment before finish: %q", after)
	}
	k.finish()
	time.Sleep(90 * time.Millisecond)
	if got := rec.Body.String(); got != after {
		t.Fatalf("a comment after the terminal event: %q became %q", after, got)
	}
}

func TestSSEFraming(t *testing.T) {
	for _, tc := range []struct {
		pieces []string
		at     bool
	}{
		{nil, true},
		{[]string{"data: x\n\n"}, true},
		{[]string{"data: x\n"}, false},
		{[]string{"data: x\r\n\r\n"}, true},
		{[]string{"data: x\r\n\r"}, true},
		{[]string{"data: x\r", "\n", "\r\n"}, true},
		{[]string{"data: x\r", "\n"}, false},
		{[]string{"data: x\n\n", "da"}, false},
		{[]string{": c\n\n"}, true},
	} {
		var f sseFraming
		for _, p := range tc.pieces {
			f.note([]byte(p))
		}
		if f.atBoundary() != tc.at {
			t.Errorf("%q: at boundary %t, want %t", tc.pieces, f.atBoundary(), tc.at)
		}
	}
}
