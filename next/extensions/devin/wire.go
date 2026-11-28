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

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

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

type field struct {
	num  int
	wire int
	val  uint64
	data []byte
}

var errBadMessage = errors.New("malformed protobuf message")

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

const (
	flagCompressed = 0x01
	flagEnd        = 0x02
	maxFrame       = 16 << 20
	maxInflated    = 64 << 20
)

func frame(msg []byte) []byte {
	out := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))
	return append(out, msg...)
}

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
		return false, nil, fmt.Errorf("a Connect frame of %d bytes is over the %d byte limit", n, maxFrame)
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

type utf8Buffer struct{ rest []byte }

func (u *utf8Buffer) feed(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	all := append(u.rest, b...)
	cut := len(all)
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

func (u *utf8Buffer) flush() string {
	s := string(u.rest)
	u.rest = nil
	return s
}
