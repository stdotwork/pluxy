package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

// Devin's API server speaks Connect (connectrpc.com) with protobuf messages. pluxy has no
// protobuf library (next/ uses only the standard library), and Devin publishes no schema, so this
// file writes and reads the wire format by field number: a message is a run of fields, each a
// tag (field number and wire type) and a value.

// Protobuf wire types.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// message is a protobuf message being written. Each method appends one field.
type message []byte

func (m message) tag(n, wire int) message {
	return binary.AppendUvarint(m, uint64(n)<<3|uint64(wire))
}

func (m message) varint(n int, v uint64) message {
	return binary.AppendUvarint(m.tag(n, wireVarint), v)
}

func (m message) bytes(n int, b []byte) message {
	m = binary.AppendUvarint(m.tag(n, wireBytes), uint64(len(b)))
	return append(m, b...)
}

func (m message) str(n int, s string) message { return m.bytes(n, []byte(s)) }

func (m message) double(n int, f float64) message {
	return binary.LittleEndian.AppendUint64(m.tag(n, wireFixed64), math.Float64bits(f))
}

// field is one field read from a message: val holds a varint or fixed value, data a
// length-delimited one.
type field struct {
	num  int
	wire int
	val  uint64
	data []byte
}

func (f field) double() float64 { return math.Float64frombits(f.val) }

var errBadMessage = errors.New("malformed protobuf message")

// parseFields reads a message's fields in order.
func parseFields(b []byte) ([]field, error) {
	var out []field
	for len(b) > 0 {
		tag, k := binary.Uvarint(b)
		if k <= 0 || tag>>3 == 0 {
			return nil, errBadMessage
		}
		b = b[k:]
		f := field{num: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case wireVarint:
			v, k := binary.Uvarint(b)
			if k <= 0 {
				return nil, errBadMessage
			}
			f.val, b = v, b[k:]
		case wireFixed64:
			if len(b) < 8 {
				return nil, errBadMessage
			}
			f.val, b = binary.LittleEndian.Uint64(b), b[8:]
		case wireBytes:
			l, k := binary.Uvarint(b)
			if k <= 0 || l > uint64(len(b)-k) {
				return nil, errBadMessage
			}
			f.data, b = b[k:k+int(l)], b[k+int(l):]
		case wireFixed32:
			if len(b) < 4 {
				return nil, errBadMessage
			}
			f.val, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return nil, fmt.Errorf("%w: wire type %d", errBadMessage, f.wire)
		}
		out = append(out, f)
	}
	return out, nil
}

// Connect streaming frames: a flags byte, the payload's length as a big-endian uint32, and the
// payload. The last frame of a stream has the end flag and carries a JSON trailer.
const (
	flagCompressed = 0x01
	flagEnd        = 0x02
	// maxFrame and maxInflated bound one frame, as today's core bounds them (16 and 64 MiB).
	maxFrame    = 16 << 20
	maxInflated = 64 << 20
)

// frame wraps a message in a Connect data frame.
func frame(msg []byte) []byte {
	out := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))
	return append(out, msg...)
}

// readFrame reads one frame. It returns io.EOF when the stream ends between frames, and
// io.ErrUnexpectedEOF when it ends inside one.
func readFrame(r io.Reader) (end bool, payload []byte, err error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return false, nil, err
	}
	flags, n := h[0], binary.BigEndian.Uint32(h[1:])
	if flags&^(flagCompressed|flagEnd) != 0 {
		return false, nil, fmt.Errorf("invalid Connect frame flags %#x", flags)
	}
	if n > maxFrame {
		return false, nil, fmt.Errorf("Connect frame of %d bytes is over the %d byte limit", n, maxFrame)
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return false, nil, err
	}
	if flags&flagCompressed != 0 {
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return false, nil, fmt.Errorf("compressed Connect frame: %w", err)
		}
		payload, err = io.ReadAll(io.LimitReader(zr, maxInflated+1))
		if err != nil {
			return false, nil, fmt.Errorf("compressed Connect frame: %w", err)
		}
		if len(payload) > maxInflated {
			return false, nil, fmt.Errorf("compressed Connect frame inflates past %d bytes", maxInflated)
		}
	}
	return flags&flagEnd != 0, payload, nil
}

// utf8Buffer holds the bytes of a character split across frames until the rest comes, so every
// piece passed on is whole UTF-8.
type utf8Buffer struct{ rest []byte }

// feed returns b, after what was held, up to its last whole character; an incomplete character
// at the end is held.
func (u *utf8Buffer) feed(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	all := append(u.rest, b...)
	cut := len(all)
	// Look back at most utf8.UTFMax-1 bytes for the start of an incomplete last character.
	for i := len(all) - 1; i >= 0 && i >= len(all)-(utf8.UTFMax-1); i-- {
		if utf8.RuneStart(all[i]) {
			if !utf8.FullRune(all[i:]) {
				cut = i
			}
			break
		}
	}
	u.rest = append([]byte(nil), all[cut:]...)
	return string(all[:cut])
}

// flush returns what is held, at the end of the reply.
func (u *utf8Buffer) flush() string {
	s := string(u.rest)
	u.rest = nil
	return s
}
