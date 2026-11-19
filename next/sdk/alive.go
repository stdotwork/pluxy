package sdk

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// aliveAfter is how long a call that asked for alive frames (protocol.Call.Alive) may go without
// a frame to core before upstream bytes make the SDK send one: core ends an attempt that has sent
// it nothing at all for as long as the account's timeouts let the upstream stay quiet, plus a
// minute, and an upstream that is still sending, only nothing the extension passes on (a model
// thinking, keep-alive comments), must not look dead to it.
const aliveAfter = 10 * time.Second

// aliveClock is the SDK's clock for alive frames; tests replace it.
var aliveClock = time.Now

// startAlive marks the stream as one whose call takes alive frames, from now.
func (s *Stream) startAlive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive = true
	s.last = aliveClock()
}

// keepAlive sends an alive frame when the call takes them, the reply has not ended, and the
// stream has written nothing for aliveAfter. Upstream reads call it when bytes arrive.
func (s *Stream) keepAlive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive || s.broken || s.seq.Done() {
		return
	}
	now := aliveClock()
	if now.Sub(s.last) < aliveAfter {
		return
	}
	if err := protocol.WriteFrame(s.w, protocol.Reply{Event: llm.Event{Type: protocol.TypeAlive}}); err != nil {
		s.broken = true
		return
	}
	s.last = now
}

// aliveSink passes the arrival of upstream bytes to the stream of the running call or turn, if
// any. A session's sink outlives its turns: its upstream connection does too.
type aliveSink struct {
	mu  sync.Mutex
	out *Stream
}

func (k *aliveSink) set(out *Stream) {
	k.mu.Lock()
	k.out = out
	k.mu.Unlock()
}

func (k *aliveSink) arrived() {
	k.mu.Lock()
	out := k.out
	k.mu.Unlock()
	if out != nil {
		out.keepAlive()
	}
}

type aliveKey struct{}

func withAlive(ctx context.Context, sink *aliveSink) context.Context {
	return context.WithValue(ctx, aliveKey{}, sink)
}

func aliveFrom(ctx context.Context) *aliveSink {
	sink, _ := ctx.Value(aliveKey{}).(*aliveSink)
	return sink
}

// aliveTransport tells the call's sink when an upstream response's headers and each piece of
// its body arrive, the body of a switched-protocols (WebSocket) response included.
type aliveTransport struct {
	next http.RoundTripper
	sink *aliveSink
}

func (t aliveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	t.sink.arrived()
	if resp.Body == nil || resp.Body == http.NoBody {
		return resp, nil // nothing to read, and the layers above tell NoBody by identity
	}
	switch b := resp.Body.(type) {
	case io.ReadWriteCloser:
		resp.Body = aliveRWBody{ReadWriteCloser: b, sink: t.sink}
	default:
		resp.Body = aliveBody{ReadCloser: b, sink: t.sink}
	}
	return resp, nil
}

type aliveBody struct {
	io.ReadCloser
	sink *aliveSink
}

func (b aliveBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.sink.arrived()
	}
	return n, err
}

type aliveRWBody struct {
	io.ReadWriteCloser
	sink *aliveSink
}

func (b aliveRWBody) Read(p []byte) (int, error) {
	n, err := b.ReadWriteCloser.Read(p)
	if n > 0 {
		b.sink.arrived()
	}
	return n, err
}
