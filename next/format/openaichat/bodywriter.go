package openaichat

import (
	"bytes"
	"encoding/json"
)

// bodyWriter writes a request body into one buffer, as the bytes json.Marshal would write for the
// same values. A string goes through an Encoder, which escapes it as Marshal does (HTML
// characters, U+2028 and U+2029 and invalid UTF-8 included) straight into the buffer, so a long
// text is not copied into a result of its own first.
type bodyWriter struct {
	buf bytes.Buffer
	enc *json.Encoder
}

func newBodyWriter(size int) *bodyWriter {
	w := &bodyWriter{}
	w.buf.Grow(size)
	w.enc = json.NewEncoder(&w.buf)
	return w
}

// str writes s as a JSON string.
func (w *bodyWriter) str(s string) {
	w.enc.Encode(s)                 // a string always encodes, and a Buffer always takes the write
	w.buf.Truncate(w.buf.Len() - 1) // the newline Encode adds
}

// raw writes b, which is JSON a marshal wrote, as Marshal writes a RawMessage: nil is null.
func (w *bodyWriter) raw(b json.RawMessage) {
	if b == nil {
		w.buf.WriteString("null")
		return
	}
	w.buf.Write(b)
}

// list writes items as a JSON array.
func (w *bodyWriter) list(items []json.RawMessage) {
	w.buf.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			w.buf.WriteByte(',')
		}
		w.raw(it)
	}
	w.buf.WriteByte(']')
}

// knownContent reports whether message writes content c itself: the types the encoder puts there.
func knownContent(c any) bool {
	switch c.(type) {
	case nil, string, *string, []json.RawMessage:
		return true
	}
	return false
}

// message writes m as marshal(m, m.raw) does, when it can without a marshal of its own: the
// message holds nothing kept from a client, and its parts and calls are compact JSON written by
// marshal.
func (w *bodyWriter) message(m *outMessage) {
	if len(m.raw) > 0 || !knownContent(m.Content) {
		w.raw(marshal(m, m.raw))
		return
	}
	w.buf.WriteString(`{"role":`)
	w.str(m.Role)
	switch c := m.Content.(type) {
	case string:
		w.buf.WriteString(`,"content":`)
		w.str(c)
	case *string:
		w.buf.WriteString(`,"content":`)
		if c == nil {
			w.buf.WriteString("null")
		} else {
			w.str(*c)
		}
	case []json.RawMessage:
		w.buf.WriteString(`,"content":`)
		if c == nil {
			w.buf.WriteString("null")
		} else {
			w.list(c)
		}
	}
	if m.ReasoningContent != "" {
		w.buf.WriteString(`,"reasoning_content":`)
		w.str(m.ReasoningContent)
	}
	if len(m.ToolCalls) > 0 {
		w.buf.WriteString(`,"tool_calls":`)
		w.list(m.ToolCalls)
	}
	if m.ToolCallID != "" {
		w.buf.WriteString(`,"tool_call_id":`)
		w.str(m.ToolCallID)
	}
	w.buf.WriteByte('}')
}

// size estimates the bytes of m's JSON, so that the body's buffer is one allocation.
func (m *outMessage) size() int {
	n := 64 + len(m.Role) + len(m.raw) + len(m.ReasoningContent) + len(m.ToolCallID)
	switch c := m.Content.(type) {
	case string:
		n += len(c)
	case *string:
		if c != nil {
			n += len(*c)
		}
	case []json.RawMessage:
		for _, p := range c {
			n += len(p) + 1
		}
	}
	for _, c := range m.ToolCalls {
		n += len(c) + 1
	}
	return n
}

// encodeBodyFast returns out as the request body's JSON, with msgs as its messages, as
// encodeBody returns it for those messages marshaled. Every message and tool is written once into
// one buffer: nothing is marshaled again, and a long text is escaped into the body itself. The
// caller has seen to it that no part or tool is JSON the client wrote (encoder.foreign), which
// a marshal would compact and escape.
func encodeBodyFast(out *outRequest, msgs []*outMessage) ([]byte, error) {
	size := 128 + len(out.Model) + 2*len(out.Stop)
	for _, m := range msgs {
		size += m.size() + 1
	}
	for _, t := range out.Tools {
		size += len(t) + 1
	}
	head, err := json.Marshal(out.tailHead)
	if err != nil {
		return nil, err
	}
	rest, err := json.Marshal(out.tailRest)
	if err != nil {
		return nil, err
	}
	size += len(head) + len(rest)
	w := newBodyWriter(size + size/32)
	w.buf.WriteString(`{"model":`)
	w.str(out.Model)
	w.buf.WriteString(`,"messages":[`)
	for i, m := range msgs {
		if i > 0 {
			w.buf.WriteByte(',')
		}
		w.message(m)
	}
	w.buf.WriteString(`],`)
	// The head always holds stream, so it starts with the object's brace and its fields follow
	// the messages' array after a comma; it ends with the brace the tail's other parts take over.
	w.buf.Write(head[1 : len(head)-1])
	if len(out.Tools) > 0 {
		w.buf.WriteString(`,"tools":`)
		w.list(out.Tools)
	}
	if len(rest) > len("{}") {
		w.buf.WriteByte(',')
		w.buf.Write(rest[1 : len(rest)-1])
	}
	w.buf.WriteByte('}')
	return w.buf.Bytes(), nil
}
