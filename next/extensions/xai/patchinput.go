package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"hash"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type inputDecoder struct {
	limit   int
	shared  *int
	n       int
	sum     hash.Hash
	pending []byte
	state   int
	head    int
	closed  bool
	input   strings.Builder
	err     error
}

const (
	decHead = iota
	decValue
	decTail
)

var headTokens = [...]string{"{", `"input"`, ":", `"`}

var errPatchInput = errors.New("custom tool arguments are not a JSON object with one input string")

var errPatchTooLarge = errors.New("custom tool arguments are too large")

var (
	maxCallArguments  = 16 << 20
	maxReplyArguments = 32 << 20
)

func (d *inputDecoder) Push(fragment string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	limit := d.limit
	if limit == 0 {
		limit = maxCallArguments
	}
	d.n += len(fragment)
	if d.shared != nil {
		*d.shared -= len(fragment)
	}
	if d.n > limit || (d.shared != nil && *d.shared < 0) {
		d.err = errPatchTooLarge
		return "", d.err
	}
	if d.sum == nil {
		d.sum = sha256.New()
	}
	d.sum.Write([]byte(fragment))
	d.pending = append(d.pending, fragment...)
	before := d.input.Len()
	for len(d.pending) > 0 && d.err == nil {
		var n int
		switch d.state {
		case decHead:
			n = d.readHead()
		case decValue:
			n = d.readValue()
		default:
			n = d.readTail()
		}
		if n == 0 {
			break
		}
		d.pending = d.pending[n:]
	}
	if d.err != nil {
		return "", d.err
	}
	return d.input.String()[before:], nil
}

func (d *inputDecoder) fail() int {
	d.err = errPatchInput
	return 0
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func (d *inputDecoder) readHead() int {
	p := d.pending
	i := 0
	for i < len(p) && isSpace(p[i]) {
		i++
	}
	tok, rest := headTokens[d.head], string(p[i:])
	if len(rest) < len(tok) {
		if !strings.HasPrefix(tok, rest) {
			return d.fail()
		}
		return i
	}
	if !strings.HasPrefix(rest, tok) {
		return d.fail()
	}
	d.head++
	if d.head == len(headTokens) {
		d.state = decValue
	}
	return i + len(tok)
}

func (d *inputDecoder) readValue() int {
	p := d.pending
	i := 0
	for i < len(p) {
		c := p[i]
		switch {
		case c == '"':
			d.state = decTail
			return i + 1
		case c == '\\':
			r, n, ok := unescape(p[i:])
			if n < 0 {
				return d.fail() + i
			}
			if !ok {
				return i
			}
			d.input.WriteRune(r)
			i += n
		case c < 0x20:
			return d.fail() + i
		case c < utf8.RuneSelf:
			d.input.WriteByte(c)
			i++
		default:
			if !utf8.FullRune(p[i:]) {
				return i
			}
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size == 1 {
				return d.fail() + i
			}
			d.input.WriteRune(r)
			i += size
		}
	}
	return i
}

func unescape(p []byte) (r rune, n int, ok bool) {
	if len(p) < 2 {
		return 0, 0, false
	}
	switch p[1] {
	case '"', '\\', '/':
		return rune(p[1]), 2, true
	case 'b':
		return '\b', 2, true
	case 'f':
		return '\f', 2, true
	case 'n':
		return '\n', 2, true
	case 'r':
		return '\r', 2, true
	case 't':
		return '\t', 2, true
	case 'u':
	default:
		return 0, -1, false
	}
	if len(p) < 6 {
		return 0, 0, false
	}
	hi, valid := hex4(p[2:6])
	switch {
	case !valid, hi >= 0xdc00 && hi <= 0xdfff:
		return 0, -1, false
	case hi < 0xd800 || hi > 0xdbff:
		return rune(hi), 6, true
	}
	if len(p) < 12 {
		if !strings.HasPrefix(`\u`, string(p[6:min(len(p), 8)])) {
			return 0, -1, false
		}
		return 0, 0, false
	}
	lo, valid := hex4(p[8:12])
	if p[6] != '\\' || p[7] != 'u' || !valid || lo < 0xdc00 || lo > 0xdfff {
		return 0, -1, false
	}
	return utf16.DecodeRune(rune(hi), rune(lo)), 12, true
}

func hex4(p []byte) (rune, bool) {
	var v rune
	for _, c := range p {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

func (d *inputDecoder) readTail() int {
	for _, c := range d.pending {
		switch {
		case isSpace(c):
		case c == '}' && !d.closed:
			d.closed = true
		default:
			return d.fail()
		}
	}
	return len(d.pending)
}

func (d *inputDecoder) complete() bool { return d.err == nil && d.state == decTail && d.closed }

func (d *inputDecoder) Input() string { return d.input.String() }

func (d *inputDecoder) Finish(final string, has bool) (input, tail string, err error) {
	if d.err != nil {
		return "", "", d.err
	}
	if !has {
		if !d.complete() {
			return "", "", errPatchInput
		}
		return d.Input(), "", nil
	}
	if d.n == 0 {
		if _, err := d.Push(final); err != nil {
			return "", "", err
		}
		if !d.complete() {
			return "", "", errPatchInput
		}
		return d.Input(), "", nil
	}
	if sum := sha256.Sum256([]byte(final)); len(final) == d.n && bytes.Equal(sum[:], d.sum.Sum(nil)) {
		if !d.complete() {
			return "", "", errPatchInput
		}
		return d.Input(), "", nil
	}
	if d.shared != nil {
		*d.shared += d.n
	}
	whole := inputDecoder{limit: d.limit, shared: d.shared}
	if _, err := whole.Push(final); err != nil {
		return "", "", err
	}
	if !whole.complete() {
		return "", "", errPatchInput
	}
	sent := d.Input()
	if !strings.HasPrefix(whole.Input(), sent) {
		return "", "", errors.New("custom tool arguments differ from the fragments that came before")
	}
	return whole.Input(), whole.Input()[len(sent):], nil
}
