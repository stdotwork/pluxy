package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/jsonscan"
)

// This file holds the per-frame work of a streamed reply, which runs once for every chunk the
// reply carries: a reader that takes one read from the socket for each frame, a writer that
// reuses its buffers, and a decoder and an encoder for the few shapes almost every reply frame
// has. Every frame they do not handle goes through encoding/json as before, and the frames on the
// wire are the same.

// frameBufSize is the size of a FrameReader's buffer: a frame that fits arrives in one read.
const frameBufSize = 4096

// FrameReader reads frames from a stream. Unlike ReadFrame it reads ahead into a buffer it keeps,
// so a frame that has arrived whole costs one read, not two, and it allocates nothing for the
// frame's bytes. Use one FrameReader for all the frames of a connection, from one goroutine at a
// time, and read the connection through it only.
type FrameReader struct {
	r     io.Reader
	buf   []byte // buf[start:end] has been read and not yet used
	start int
	end   int
}

// NewFrameReader returns a FrameReader that reads from r.
func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

// fill reads until at least n bytes are buffered. A frame the buffer cannot hold is read into a
// buffer of its own size, which the reader does not keep.
func (f *FrameReader) fill(n int) error {
	if f.end-f.start >= n {
		return nil
	}
	if f.buf == nil {
		f.buf = make([]byte, frameBufSize)
	}
	if n > len(f.buf) {
		// Too big for the kept buffer: move what is buffered into one that fits.
		big := make([]byte, n)
		f.end = copy(big, f.buf[f.start:f.end])
		f.start = 0
		f.buf = big
	} else if f.start+n > len(f.buf) {
		f.end = copy(f.buf, f.buf[f.start:f.end])
		f.start = 0
	}
	for f.end-f.start < n {
		m, err := f.r.Read(f.buf[f.end:])
		f.end += m
		if f.end-f.start >= n {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Read reads one frame into v, as ReadFrame does, with the same errors.
func (f *FrameReader) Read(v any) error {
	if err := f.fill(4); err != nil {
		if err == io.EOF && f.end-f.start > 0 {
			err = io.ErrUnexpectedEOF
		}
		if err == io.ErrUnexpectedEOF {
			return fmt.Errorf("protocol: truncated frame header: %w", err)
		}
		return err
	}
	n := binary.BigEndian.Uint32(f.buf[f.start:])
	if n > MaxFrame {
		return ErrFrameTooLarge
	}
	total := 4 + int(n)
	if err := f.fill(total); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("protocol: truncated frame: %w", err)
	}
	body := f.buf[f.start+4 : f.start+total]
	f.start += total
	var err error
	if r, ok := v.(*Reply); ok && decodeReply(body, r) {
		// decoded
	} else {
		err = json.Unmarshal(body, v)
	}
	if f.start == f.end {
		f.start, f.end = 0, 0
		if len(f.buf) > frameBufSize {
			f.buf = nil // a frame of its own size is not kept
		}
	}
	return err
}

// decodeReply decodes the reply frame body into r when body is a JSON object that has only the
// members type, index, delta and raw, each once and spelled as the encoder writes them, with
// values encoding/json decodes the same way. It reports false, having changed nothing, for any
// other frame, which the caller decodes with encoding/json.
func decodeReply(body []byte, r *Reply) bool {
	members, ok := jsonscan.Object(body)
	if !ok || len(members) == 0 || len(members) > 4 {
		return false
	}
	var (
		typ, delta string
		index      int
		raw        json.RawMessage
		seen       uint8
	)
	for _, m := range members {
		var bit uint8
		switch string(m.Key) {
		case `"type"`:
			bit = 1
			if !jsonscan.IsString(m.Value) {
				return false
			}
			t, ok := jsonscan.Unquote(m.Value)
			if !ok {
				return false
			}
			typ = t
		case `"index"`:
			bit = 2
			i, ok := plainInt(m.Value)
			if !ok {
				return false
			}
			index = i
		case `"delta"`:
			bit = 4
			if !jsonscan.IsString(m.Value) {
				return false
			}
			t, ok := jsonscan.Unquote(m.Value)
			if !ok {
				return false
			}
			delta = t
		case `"raw"`:
			bit = 8
			if jsonscan.IsNull(m.Value) {
				return false
			}
			raw = m.Value
		default:
			return false
		}
		if seen&bit != 0 {
			return false
		}
		seen |= bit
	}
	if seen&1 != 0 {
		r.Type = typ
	}
	if seen&2 != 0 {
		r.Index = index
	}
	if seen&4 != 0 {
		r.Delta = delta
	}
	if seen&8 != 0 {
		r.Raw = append(json.RawMessage(nil), raw...)
	}
	return true
}

// plainInt returns the integer a JSON number literal spells when it has no fraction or exponent
// and fits an int; encoding/json decodes any other number into an int with an error.
func plainInt(v []byte) (int, bool) {
	if len(v) == 0 || len(v) > 18 {
		return 0, false
	}
	for i, c := range v {
		if (c < '0' || c > '9') && !(i == 0 && c == '-' && len(v) > 1) {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

// framePool holds the buffers WriteFrame encodes frames in. A buffer that grew past maxPooledFrame
// (a request that carries a whole conversation) is not kept.
var framePool = sync.Pool{New: func() any { return &frameBuf{b: make([]byte, 0, 1024)} }}

const maxPooledFrame = 64 << 10

type frameBuf struct {
	b   []byte
	buf bytes.Buffer // the encoder's, for frames the fast encoder does not handle
}

// appendReply appends the JSON of r to dst as encoding/json writes it (escaping HTML off) when r
// has only a type, index, delta and raw. It reports false, leaving dst as it was, otherwise or
// when raw is not valid JSON, which encoding/json reports with its own error.
func appendReply(dst []byte, r *Reply) ([]byte, bool) {
	e := &r.Event
	if e.Response != nil || e.Item != nil || e.Opaque != nil || len(e.Citations) != 0 || e.Finish != nil ||
		e.Usage != nil || len(e.Sources) != 0 || len(e.CodeExecutions) != 0 || e.Media != nil ||
		e.Candidate != nil || e.Error != nil ||
		r.Info != nil || r.Prompt != nil || r.Grant != nil || r.Models != nil || r.Check != nil ||
		r.Quota != nil || r.Diagnostic != nil {
		return dst, false
	}
	mark := len(dst)
	dst = append(dst, `{"type":`...)
	dst = appendJSONString(dst, e.Type)
	dst = append(dst, `,"index":`...)
	dst = strconv.AppendInt(dst, int64(e.Index), 10)
	if e.Delta != "" {
		dst = append(dst, `,"delta":`...)
		dst = appendJSONString(dst, e.Delta)
	}
	if len(e.Raw) != 0 {
		dst = append(dst, `,"raw":`...)
		var ok bool
		if dst, ok = appendCompactJSON(dst, e.Raw); !ok {
			return dst[:mark], false
		}
	}
	return append(dst, '}'), true
}

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a JSON string literal the way encoding/json writes it with HTML
// escaping off: control characters, quote and backslash escaped, invalid UTF-8 replaced with
// U+FFFD, and U+2028 and U+2029 escaped.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= ' ' && b != '"' && b != '\\' {
				i++
				continue
			}
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
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, "\\ufffd"...)
			i += size
			start = i
			continue
		}
		if c == ' ' || c == ' ' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[c&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// appendCompactJSON appends raw, which must be one valid JSON value, without the whitespace
// outside its strings, as encoding/json writes a json.RawMessage.
func appendCompactJSON(dst, raw []byte) ([]byte, bool) {
	if compact(raw) {
		if !jsonscan.Valid(raw) {
			return dst, false
		}
		return append(dst, raw...), true
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return dst, false
	}
	return append(dst, b.Bytes()...), true
}

// compact reports whether raw has no JSON whitespace outside its strings. It does not check that
// raw is valid.
func compact(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			return false
		}
	}
	return true
}
