package openaichat

import (
	"encoding/json"
	"unicode/utf8"
)

// This file writes the request body into one slice, as the bytes json.Marshal writes for the same
// values. json.Marshal builds each value in a buffer of its own and copies it out, so a long text
// was escaped into a buffer, copied to a result, copied into its message, copied into the body.
// Here a text is escaped once, into the slice that is sent.

const hexDigits = "0123456789abcdef"

// escapeClass says what appendString does with a byte: 0 writes it as it is, 1 escapes it, 2
// starts a multi-byte rune that may need escaping. It matches encoding/json's HTML-safe set; the
// test compares the two for every byte value.
var escapeClass = func() (t [256]uint8) {
	for b := range t {
		switch {
		case b >= utf8.RuneSelf:
			t[b] = 2
		case b < 0x20, b == '"', b == '\\', b == '<', b == '>', b == '&':
			t[b] = 1
		}
	}
	return t
}()

// appendString appends s to dst as a JSON string, as json.Marshal writes it: <, > and & are
// escaped, so are U+2028 and U+2029, and a byte that is not valid UTF-8 becomes the escape for U+FFFD.
func appendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		switch escapeClass[b] {
		case 0:
			i++
			continue
		case 1:
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}
			i++
		default:
			c, size := utf8.DecodeRuneInString(s[i:])
			switch {
			case c == utf8.RuneError && size == 1:
				dst = append(dst, s[start:i]...)
				dst = append(dst, `\ufffd`...)
			case c == '\u2028' || c == '\u2029':
				dst = append(dst, s[start:i]...)
				dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[c&0xF])
			default:
				i += size
				continue
			}
			i += size
		}
		start = i
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// quoted estimates the bytes appendString writes for s: the text, its quotes, and room for the
// escapes of a text that has a few.
func quoted(s string) int { return len(s) + len(s)/16 + 2 }

// appendRaw appends b, which is JSON a marshal wrote, as Marshal writes a RawMessage: nil is null.
func appendRaw(dst []byte, b json.RawMessage) []byte {
	if b == nil {
		return append(dst, "null"...)
	}
	return append(dst, b...)
}

// appendList appends items as a JSON array.
func appendList(dst []byte, items []json.RawMessage) []byte {
	dst = append(dst, '[')
	for i, it := range items {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendRaw(dst, it)
	}
	return append(dst, ']')
}

// partJSON returns p as marshal(p, raw) does. A part with no kept fields is written directly into
// a slice of its own size.
func partJSON(p outPart, raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 {
		return marshal(p, raw)
	}
	n := 64 + len(p.Type)
	if p.Text != nil {
		n += quoted(*p.Text)
	}
	if v := p.ImageURL; v != nil {
		n += quoted(v.URL) + quoted(v.Detail) + 32
	}
	if v := p.File; v != nil {
		n += quoted(v.FileID) + quoted(v.FileData) + quoted(v.Filename) + 64
	}
	if v := p.InputAudio; v != nil {
		n += quoted(v.Data) + quoted(v.Format) + 32
	}
	b := make([]byte, 0, n)
	b = append(b, `{"type":`...)
	b = appendString(b, p.Type)
	if p.Text != nil {
		b = append(b, `,"text":`...)
		b = appendString(b, *p.Text)
	}
	if v := p.ImageURL; v != nil {
		b = append(b, `,"image_url":{"url":`...)
		b = appendString(b, v.URL)
		if v.Detail != "" {
			b = append(b, `,"detail":`...)
			b = appendString(b, v.Detail)
		}
		b = append(b, '}')
	}
	if v := p.File; v != nil {
		b = append(b, `,"file":{`...)
		sep := ""
		for _, f := range [...]struct{ key, val string }{{`"file_id":`, v.FileID}, {`"file_data":`, v.FileData}, {`"filename":`, v.Filename}} {
			if f.val != "" {
				b = append(append(b, sep...), f.key...)
				b = appendString(b, f.val)
				sep = ","
			}
		}
		b = append(b, '}')
	}
	if v := p.InputAudio; v != nil {
		b = append(b, `,"input_audio":{"data":`...)
		b = appendString(b, v.Data)
		if v.Format != "" {
			b = append(b, `,"format":`...)
			b = appendString(b, v.Format)
		}
		b = append(b, '}')
	}
	return append(b, '}')
}

// chatToolCallJSON returns tc as marshal(tc, raw) does, written directly when raw adds nothing.
func chatToolCallJSON(tc chatToolCall, raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 {
		return marshal(tc, raw)
	}
	b := make([]byte, 0, 96+quoted(tc.ID)+quoted(tc.Type)+quoted(tc.Function.Name)+quoted(tc.Function.Arguments))
	b = append(b, `{"id":`...)
	b = appendString(b, tc.ID)
	b = append(b, `,"type":`...)
	b = appendString(b, tc.Type)
	b = append(b, `,"function":{"name":`...)
	b = appendString(b, tc.Function.Name)
	b = append(b, `,"arguments":`...)
	b = appendString(b, tc.Function.Arguments)
	return append(b, "}}"...)
}

// customToolCallJSON returns tc as marshal(tc, raw) does, written directly when raw adds nothing.
func customToolCallJSON(tc customToolCall, raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 {
		return marshal(tc, raw)
	}
	b := make([]byte, 0, 96+quoted(tc.ID)+quoted(tc.Type)+quoted(tc.Custom.Name)+quoted(tc.Custom.Input))
	b = append(b, `{"id":`...)
	b = appendString(b, tc.ID)
	b = append(b, `,"type":`...)
	b = appendString(b, tc.Type)
	b = append(b, `,"custom":{"name":`...)
	b = appendString(b, tc.Custom.Name)
	b = append(b, `,"input":`...)
	b = appendString(b, tc.Custom.Input)
	return append(b, "}}"...)
}

// knownContent reports whether appendMessage writes content c itself: the types the encoder puts
// there.
func knownContent(c any) bool {
	switch c.(type) {
	case nil, string, *string, []json.RawMessage:
		return true
	}
	return false
}

// appendMessage appends m as marshal(m, m.raw) writes it, when it can without a marshal of its
// own: the message holds nothing kept from a client, and its parts and calls are compact JSON
// written by marshal or by the writers above.
func appendMessage(dst []byte, m *outMessage) []byte {
	if len(m.raw) > 0 || !knownContent(m.Content) {
		return appendRaw(dst, marshal(m, m.raw))
	}
	dst = append(dst, `{"role":`...)
	dst = appendString(dst, m.Role)
	switch c := m.Content.(type) {
	case string:
		dst = append(dst, `,"content":`...)
		dst = appendString(dst, c)
	case *string:
		dst = append(dst, `,"content":`...)
		if c == nil {
			dst = append(dst, "null"...)
		} else {
			dst = appendString(dst, *c)
		}
	case []json.RawMessage:
		dst = append(dst, `,"content":`...)
		if c == nil {
			dst = append(dst, "null"...)
		} else {
			dst = appendList(dst, c)
		}
	}
	if m.ReasoningContent != "" {
		dst = append(dst, `,"reasoning_content":`...)
		dst = appendString(dst, m.ReasoningContent)
	}
	if len(m.ToolCalls) > 0 {
		dst = append(dst, `,"tool_calls":`...)
		dst = appendList(dst, m.ToolCalls)
	}
	if m.ToolCallID != "" {
		dst = append(dst, `,"tool_call_id":`...)
		dst = appendString(dst, m.ToolCallID)
	}
	return append(dst, '}')
}

// size estimates the bytes of m's JSON, so that the body's slice is one allocation.
func (m *outMessage) size() int {
	n := 64 + len(m.Role) + len(m.raw) + quoted(m.ReasoningContent) + quoted(m.ToolCallID)
	switch c := m.Content.(type) {
	case string:
		n += quoted(c)
	case *string:
		if c != nil {
			n += quoted(*c)
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
// one slice: nothing is marshaled again, and a long text is escaped into the body itself. The
// caller has seen to it that no part or tool is JSON the client wrote (encoder.foreign), which
// a marshal would compact and escape.
func encodeBodyFast(out *outRequest, msgs []*outMessage) ([]byte, error) {
	head, err := json.Marshal(out.tailHead)
	if err != nil {
		return nil, err
	}
	rest, err := json.Marshal(out.tailRest)
	if err != nil {
		return nil, err
	}
	size := 64 + quoted(out.Model) + len(head) + len(rest)
	for _, m := range msgs {
		size += m.size() + 1
	}
	for _, t := range out.Tools {
		size += len(t) + 1
	}
	b := make([]byte, 0, size)
	b = append(b, `{"model":`...)
	b = appendString(b, out.Model)
	b = append(b, `,"messages":[`...)
	for i, m := range msgs {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendMessage(b, m)
	}
	b = append(b, `],`...)
	// The head always holds stream, so it starts with the object's brace and its fields follow
	// the messages' array after a comma; it ends with the brace the tail's other parts take over.
	b = append(b, head[1:len(head)-1]...)
	if len(out.Tools) > 0 {
		b = append(b, `,"tools":`...)
		b = appendList(b, out.Tools)
	}
	if len(rest) > len("{}") {
		b = append(b, ',')
		b = append(b, rest[1:len(rest)-1]...)
	}
	return append(b, '}'), nil
}
