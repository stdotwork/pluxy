package protocol

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// benchCall is a native-only generate call carrying a client body of about size bytes.
func benchCall(size int) Call {
	var b strings.Builder
	b.WriteString(`{"model":"claude-x","max_tokens":4096,"stream":true,"messages":[`)
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"user","content":[{"type":"text","text":"line %d of a prompt with <tags> & \"quotes\" and a little more text to fill the message out"}]}`, i)
	}
	b.WriteString(`]}`)
	return Call{Type: TypeCall, ID: "1", Op: OpGenerate, NativeOnly: true, Account: &Account{ID: "a"},
		Request: &llm.Request{Model: "claude-x", Stream: true,
			Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: []byte(b.String()), Headers: map[string]string{"anthropic-version": "2023-06-01"}}}}
}

func BenchmarkWriteCallFrame(b *testing.B) {
	call := benchCall(100 << 10)
	b.ReportAllocs()
	b.SetBytes(int64(len(call.Request.Native.Body)))
	for b.Loop() {
		if err := WriteFrame(io.Discard, call); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadCallFrame(b *testing.B) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, benchCall(100<<10)); err != nil {
		b.Fatal(err)
	}
	frame := buf.Bytes()
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		var c Call
		if err := ReadFrame(bytes.NewReader(frame), &c); err != nil {
			b.Fatal(err)
		}
	}
}
