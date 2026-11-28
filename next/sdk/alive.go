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

const aliveAfter = 10 * time.Second

var aliveClock = time.Now

func (s *Stream) startAlive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive = true
	s.last = aliveClock()
}

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
		return resp, nil
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
