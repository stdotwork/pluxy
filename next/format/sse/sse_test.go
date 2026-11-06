package sse

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestReader(t *testing.T) {
	// Examples follow the WHATWG event stream interpretation rules.
	stream := ": comment\n" +
		"data: first\n\n" +
		"event: delta\r\nid: 7\r\ndata: line one\r\ndata:line two\r\n\r\n" +
		"retry: 1000\n\n" + // no data: dispatches nothing
		"data: [DONE]\n\n" +
		"data: cut off"
	// OneByteReader splits every event across many reads.
	r := NewReader(iotest.OneByteReader(strings.NewReader(stream)))
	want := []Event{
		{Data: "first"},
		{Name: "delta", ID: "7", Data: "line one\nline two"},
		{Data: "[DONE]"},
	}
	for i, w := range want {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if got != w {
			t.Fatalf("event %d = %+v, want %+v", i, got, w)
		}
	}
	if _, err := r.Next(); err != io.ErrUnexpectedEOF {
		t.Fatalf("after a cut-off event: err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReaderLimit(t *testing.T) {
	r := NewReader(strings.NewReader("data: " + strings.Repeat("x", 100) + "\n\n"))
	r.max = 50
	if _, err := r.Next(); err != ErrEventTooLarge {
		t.Fatalf("err = %v, want ErrEventTooLarge", err)
	}
}

func TestReaderLineEndings(t *testing.T) {
	// The standard's end-of-line is CRLF, LF or a lone CR, and a stream may mix them.
	for name, stream := range map[string]string{
		"CR":         "data: hello\rdata: world\r\rdata: [DONE]\r\r",
		"mixed":      "data: hello\r\ndata: world\r\rdata: [DONE]\n\n",
		"CR then LF": "data: hello\rdata: world\r\n\ndata: [DONE]\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := NewReader(iotest.OneByteReader(strings.NewReader(stream)))
			for i, want := range []string{"hello\nworld", "[DONE]"} {
				got, err := r.Next()
				if err != nil {
					t.Fatalf("event %d: %v", i, err)
				}
				if got.Data != want {
					t.Fatalf("event %d data = %q, want %q", i, got.Data, want)
				}
			}
			if _, err := r.Next(); err != io.EOF {
				t.Fatalf("at the end: err = %v, want io.EOF", err)
			}
		})
	}
}

func TestReaderCRDispatchesWithoutMoreInput(t *testing.T) {
	// An event ended by CR CR must be returned at once, not when the next byte arrives.
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() { _, _ = io.WriteString(pw, "data: now\r\r") }()
	r := NewReader(pr)
	got := make(chan Event, 1)
	go func() {
		ev, _ := r.Next()
		got <- ev
	}()
	select {
	case ev := <-got:
		if ev.Data != "now" {
			t.Fatalf("data = %q, want %q", ev.Data, "now")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event ended by CR CR was not returned while the stream stayed open")
	}
}

func TestReaderLimitIsPerEvent(t *testing.T) {
	// Keepalive comments are events of their own; their bytes must not count toward the next one.
	stream := strings.Repeat(": keepalive\n\n", 100) + "data: ok\n\n"
	r := NewReader(strings.NewReader(stream))
	r.max = 50
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("err = %v, want the data event", err)
	}
	if ev.Data != "ok" {
		t.Fatalf("data = %q, want %q", ev.Data, "ok")
	}
}
