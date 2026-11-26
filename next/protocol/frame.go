package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/stdotwork/pluxy/next/internal/jsonscan"
)

// This file holds the per-frame work of reading a streamed reply, which runs once for every chunk
// the reply carries: a reader that takes one read from the socket for each frame, and a decoder
// for the one shape nearly every reply frame has. Every frame the decoder does not handle goes
// through encoding/json as before.

// frameBufSize is the size of a FrameReader's buffer: a frame that fits arrives in one read.
const frameBufSize = 4096

// FrameReader reads frames from a stream. Unlike ReadFrame it reads ahead into a buffer it keeps,
// so a frame that has arrived whole costs one read, not two. It decodes a frame from that buffer,
// and no value it fills holds a slice of the buffer. Use one FrameReader for all the frames of a
// connection, from one goroutine at a time, and read the connection through it only.
type FrameReader struct {
	r     io.Reader
	buf   []byte // buf[start:end] has been read and not yet used
	start int
	end   int
}

// NewFrameReader returns a FrameReader that reads from r.
func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

// fill reads until at least n bytes are buffered. A frame the buffer cannot hold is read into a
// buffer of its own size, which the reader keeps only while that frame is being read: Read drops it
// once the frame is used, and fill drops it when the read fails.
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
			if len(f.buf) > frameBufSize {
				// A frame of its own size is not kept, whatever became of the frame.
				f.buf = nil
				f.start, f.end = 0, 0
			}
			return err
		}
	}
	return nil
}

// Read reads one frame into v, as ReadFrame does, with the same value and, for a connection that
// reads as it should, the same errors. The first error a read of the connection returns ends its
// use: what the reader holds after one is not defined by ReadFrame, so read no more frames. In
// particular, after ErrFrameTooLarge the frame's header stays buffered and a second Read repeats
// that error, where ReadFrame would read on into the frame's body.
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
	if r, ok := v.(*Reply); ok && r != nil && decodeReply(body, r) {
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
// values encoding/json decodes the same way. It copies what it keeps out of body. It reports
// false, having changed nothing, for any other frame, which the caller decodes with
// encoding/json.
func decodeReply(body []byte, r *Reply) bool {
	var space [4]jsonscan.Member // an object of up to four members costs no allocation
	members, ok := jsonscan.AppendObject(space[:0], body)
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
	if err != nil || int64(int(n)) != n {
		return 0, false
	}
	return int(n), true
}
