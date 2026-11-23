package host

import (
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// bufferedStream returns a Stream that has taken response.start from a pipe whose other end wrote
// response.start and finish in one write, so the finish is in the stream's read-ahead buffer.
func bufferedStream(t *testing.T, ctx context.Context) *Stream {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	var both bytes.Buffer
	for _, ev := range []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "m"}}, {Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}} {
		if err := protocol.WriteFrame(&both, protocol.Reply{Event: ev}); err != nil {
			t.Fatal(err)
		}
	}
	go func() { _, _ = theirs.Write(both.Bytes()) }()
	s := &Stream{conn: ours, ctx: ctx, ext: &Extension{manifest: &Manifest{ID: "hosttest/buffered"}}, redact: protocol.NewRedactor(), account: "a"}
	if ev := s.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("first event %+v, want response.start", ev)
	}
	return s
}

// A call whose context ends after response.start gets cancelled, though the extension's finish
// frame had already arrived in the reader's buffer: the connection a read of it would have failed
// on is closed.
func TestBufferedFrameAfterCancelIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := bufferedStream(t, ctx)
	cancel()
	ev := s.Next()
	if ev.Type != llm.EventError || ev.Error == nil || ev.Error.Code != llm.CodeCancelled {
		t.Fatalf("event after cancel %+v, want an error with code %s", ev, llm.CodeCancelled)
	}
}

// After Close, Next does not deliver a frame that was already buffered: it ends as a read of the
// closed connection does, with the extension ending the call without finishing it.
func TestBufferedFrameAfterCloseIsNotDelivered(t *testing.T) {
	s := bufferedStream(t, context.Background())
	s.Close()
	ev := s.Next()
	if ev.Type != llm.EventError || ev.Error == nil || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Fatalf("event after Close %+v, want an error with code %s", ev, llm.CodeExtensionCrashed)
	}
}
