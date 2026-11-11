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

// inputDecoder reads the patch text out of the arguments of an apply_patch function call,
// {"input":"<patch text>"}, while the arguments arrive in fragments. It accepts only that shape: an
// object with one field named input whose value is a string, and nothing after the object. Text
// is handed out as soon as a whole character of it has arrived, so a fragment may end inside an
// escape or inside a character. One decoder reads one call.
//
// A decoder keeps the patch text and the bytes of a character that has not all arrived, nothing
// else: of the fragments it keeps their number of bytes and a hash, to compare with the final
// arguments. It refuses arguments of more than limit bytes (maxCallArguments when limit is 0), and,
// with shared set, more than the bytes left there, which a reply's calls share.
type inputDecoder struct {
	limit   int
	shared  *int
	n       int       // bytes of fragments received
	sum     hash.Hash // their hash
	pending []byte    // received bytes not read yet
	state   int
	head    int  // how many of headTokens are read
	closed  bool // the closing brace is read
	input   strings.Builder
	err     error
}

const (
	decHead  = iota // before the value's opening quote
	decValue        // inside the value
	decTail         // after the value
)

// headTokens are what comes before the patch text, each with optional white space before it.
var headTokens = [...]string{"{", `"input"`, ":", `"`}

var errPatchInput = errors.New("apply_patch arguments are not a JSON object with one input string")

var errPatchTooLarge = errors.New("apply_patch arguments are too large")

// The most argument bytes one apply_patch call may have, and the most all of a reply's calls may
// have; a patch of several megabytes is the largest anyone writes.
var (
	maxCallArguments  = 16 << 20
	maxReplyArguments = 32 << 20
)

// Push reads the next fragment and returns the patch text it completed, which may be empty.
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

// readHead reads white space and then the next head token, once it has all arrived, and returns
// how many bytes it read.
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
		return i // the token has not all arrived
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

// readValue reads the whole characters of the value at the start of pending and returns how many
// bytes it read. A character that has not all arrived stays in pending.
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

// unescape reads one escape sequence at the start of p, which starts with a backslash. It returns
// the character and the bytes the escape takes; ok is false when the escape has not all arrived,
// and n is negative when it is not valid JSON. A \u escape of a high surrogate takes the low
// surrogate's escape after it.
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
		return 0, -1, false // not hex, or a low surrogate with no high one before it
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

// readTail reads what may follow the value: white space and the closing brace.
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

// complete reports whether the arguments read so far are a whole object.
func (d *inputDecoder) complete() bool { return d.err == nil && d.state == decTail && d.closed }

// Input is the patch text read so far.
func (d *inputDecoder) Input() string { return d.input.String() }

// Finish checks the final arguments xAI reports for the call and returns the whole patch text and
// the part of it the fragments did not carry; has is false when xAI reports none, which leaves the
// fragments as they came. A call that came in no fragments has no such part: its text is sent
// whole when the call ends. The final arguments must hold the text the fragments carried, as a
// prefix.
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
	// The final arguments take the place of the fragments, in the reply's budget too.
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
		return "", "", errors.New("apply_patch arguments differ from the fragments that came before")
	}
	return whole.Input(), whole.Input()[len(sent):], nil
}
