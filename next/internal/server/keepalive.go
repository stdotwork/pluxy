package server

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// keepAliveAfter is how long a streaming client may go without a byte before core writes it an
// SSE comment: clients and the proxies between them drop a stream that is quiet for long, and a
// model can think for minutes without a word. Tests shorten it.
var keepAliveAfter = 15 * time.Second

// keepAliveComment is what core writes. The SSE format makes a line that starts with a colon a
// comment, which readers that follow it skip; Google's Python Gemini SDK does not, so the Gemini
// route gets none.
const keepAliveComment = ": keep-alive\n\n"

// keepAlive writes a streamed reply's bytes to the client and, whenever keepAliveAfter passes with
// nothing written, a keep-alive comment, flushed. It writes the comment only between events, so a
// reply whose pieces cut an event (an endpoint's passed-on body) is never broken by it. Writes and
// flushes are serialised with the timer's; stop it before the handler returns.
type keepAlive struct {
	mu        sync.Mutex
	w         io.Writer
	rc        *http.ResponseController
	timer     *time.Timer
	framing   sseFraming
	lastWrite time.Time
	finished  bool
	stopped   bool
}

func startKeepAlive(w io.Writer, rc *http.ResponseController) *keepAlive {
	k := &keepAlive{w: w, rc: rc, lastWrite: time.Now()}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.timer = time.AfterFunc(keepAliveAfter, k.fire)
	return k
}

func (k *keepAlive) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	n, err := k.w.Write(p)
	k.framing.note(p[:n])
	if n > 0 {
		k.lastWrite = time.Now()
	}
	if !k.finished && !k.stopped {
		k.timer.Reset(keepAliveAfter)
	}
	return n, err
}

func (k *keepAlive) Flush() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rc.Flush()
}

func (k *keepAlive) fire() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped || k.finished {
		return
	}
	if remaining := keepAliveAfter - time.Since(k.lastWrite); remaining > 0 {
		k.timer.Reset(remaining)
		return
	}
	if k.framing.atBoundary() {
		if _, err := io.WriteString(k.w, keepAliveComment); err != nil {
			return // the client went away; the reply's next write sees it
		}
		k.lastWrite = time.Now()
		if k.rc.Flush() != nil {
			return
		}
	}
	k.timer.Reset(keepAliveAfter)
}

// finish marks the reply as at its terminal event; a timer callback that runs after this point
// writes nothing.
func (k *keepAlive) finish() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.finished = true
	k.timer.Stop()
}

// stop ends the comments; none is written once it returns.
func (k *keepAlive) stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopped = true
	k.timer.Stop()
}

// sseFraming follows SSE framing over the bytes written so far, to tell whether they end between
// events: at the start, or after a blank line. It reads only line ends, so a piece that ends inside
// a line, a CRLF pair or a UTF-8 character leaves it inside the event. (The rule follows
// OmniRoute's sseHeartbeat.ts; see THIRD_PARTY_NOTICES.md.)
type sseFraming struct {
	inside    bool // not at an event boundary
	lineBytes bool // the current line has bytes
	cr        bool // the last byte was a CR, whose LF would end the same line
}

func (f *sseFraming) note(p []byte) {
	for _, b := range p {
		if f.cr {
			f.cr = false
			if b == '\n' {
				continue
			}
		}
		if b == '\n' || b == '\r' {
			f.inside = f.lineBytes
			f.lineBytes = false
			f.cr = b == '\r'
		} else {
			f.lineBytes = true
			f.inside = true
		}
	}
}

func (f *sseFraming) atBoundary() bool { return !f.inside }
