package sse

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
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
