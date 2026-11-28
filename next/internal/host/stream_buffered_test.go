package host

import (
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

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

func TestBufferedFrameAfterCancelIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := bufferedStream(t, ctx)
	cancel()
	ev := s.Next()
	if ev.Type != llm.EventError || ev.Error == nil || ev.Error.Code != llm.CodeCancelled {
		t.Fatalf("event after cancel %+v, want an error with code %s", ev, llm.CodeCancelled)
	}
}

func TestBufferedFrameAfterCloseIsNotDelivered(t *testing.T) {
	s := bufferedStream(t, context.Background())
	s.Close()
	ev := s.Next()
	if ev.Type != llm.EventError || ev.Error == nil || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Fatalf("event after Close %+v, want an error with code %s", ev, llm.CodeExtensionCrashed)
	}
}
