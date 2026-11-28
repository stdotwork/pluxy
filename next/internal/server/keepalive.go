package server

import (
	"io"
	"net/http"
	"sync"
	"time"
)

var keepAliveAfter = 15 * time.Second

// The SSE format makes a line that starts with a colon a comment, which readers that follow it
// skip; Google's Python Gemini SDK does not, so Gemini routes get none.
const keepAliveComment = ": keep-alive\n\n"

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
			return
		}
		k.lastWrite = time.Now()
		if k.rc.Flush() != nil {
			return
		}
	}
	k.timer.Reset(keepAliveAfter)
}

func (k *keepAlive) finish() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.finished = true
	k.timer.Stop()
}

func (k *keepAlive) stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopped = true
	k.timer.Stop()
}

// The rule follows OmniRoute's sseHeartbeat.ts; see THIRD_PARTY_NOTICES.md.
type sseFraming struct {
	inside    bool
	lineBytes bool
	cr        bool
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
