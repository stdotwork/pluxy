package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/stdotwork/pluxy/next/internal/jsonscan"
)

const frameBufSize = 4096

// FrameReader reads frames from a stream. Unlike ReadFrame it reads ahead into a buffer it keeps,
// and no value it fills holds a slice of the buffer. Use one FrameReader for all the frames of a
// connection, from one goroutine at a time, and read the connection through it only.
type FrameReader struct {
	r     io.Reader
	buf   []byte
	start int
	end   int
}

func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

func (f *FrameReader) fill(n int) error {
	if f.end-f.start >= n {
		return nil
	}
	if f.buf == nil {
		f.buf = make([]byte, frameBufSize)
	}
	if n > len(f.buf) {
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
	} else {
		err = json.Unmarshal(body, v)
	}
	if f.start == f.end {
		f.start, f.end = 0, 0
		if len(f.buf) > frameBufSize {
			f.buf = nil
		}
	}
	return err
}

func decodeReply(body []byte, r *Reply) bool {
	var space [4]jsonscan.Member
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
