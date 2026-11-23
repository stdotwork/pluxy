// Package jsonscan reads the structure of JSON text without decoding it: whether the text is
// valid, and the members of an object or the elements of an array as slices of the text. It
// accepts exactly the text encoding/json accepts (invalid UTF-8 in strings included, as
// encoding/json replaces it when decoding), so a caller can check text here and leave anything
// it does not handle to encoding/json with the same outcome. It builds no values, so it is many
// times faster than decoding into json.RawMessage and copies nothing.
package jsonscan

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxDepth is how deeply arrays and objects may nest, as encoding/json allows.
const maxDepth = 10000

// Valid reports whether data is one JSON value with optional whitespace around it, as
// json.Valid does.
func Valid(data []byte) bool {
	i := skipSpace(data, 0)
	end := valueEnd(data, i, 1)
	return end >= 0 && skipSpace(data, end) == len(data)
}

// Member is one member of a JSON object: its name as written, quotes and escapes included, and
// its value. Both are slices of the object's text whose capacity ends where they do, so
// appending to one never writes into the text.
type Member struct {
	Key, Value []byte
}

// Object returns the members of the JSON object data in their order, duplicates included, and
// whether data is one valid JSON object with optional whitespace around it.
func Object(data []byte) ([]Member, bool) { return AppendObject(nil, data) }

// AppendObject is Object with the members appended to dst, so a caller that keeps a small array
// for them allocates nothing for an object that fits it. On false, the returned slice is not to be
// used: it may be nil or dst.
func AppendObject(dst []Member, data []byte) ([]Member, bool) {
	i := skipSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return nil, false
	}
	members := dst
	i = skipSpace(data, i+1)
	if i < len(data) && data[i] == '}' {
		return members, skipSpace(data, i+1) == len(data)
	}
	for {
		if i >= len(data) || data[i] != '"' {
			return nil, false
		}
		kEnd := stringEnd(data, i)
		if kEnd < 0 {
			return nil, false
		}
		key := data[i:kEnd:kEnd]
		i = skipSpace(data, kEnd)
		if i >= len(data) || data[i] != ':' {
			return nil, false
		}
		i = skipSpace(data, i+1)
		vEnd := valueEnd(data, i, 2)
		if vEnd < 0 {
			return nil, false
		}
		members = append(members, Member{Key: key, Value: data[i:vEnd:vEnd]})
		i = skipSpace(data, vEnd)
		if i >= len(data) {
			return nil, false
		}
		switch data[i] {
		case ',':
			i = skipSpace(data, i+1)
		case '}':
			if skipSpace(data, i+1) != len(data) {
				return nil, false
			}
			return members, true
		default:
			return nil, false
		}
	}
}

// Array returns the elements of the JSON array data in their order, and whether data is one
// valid JSON array with optional whitespace around it. Each element is a slice of data whose
// capacity ends where it does.
func Array(data []byte) ([][]byte, bool) {
	i := skipSpace(data, 0)
	if i >= len(data) || data[i] != '[' {
		return nil, false
	}
	var elems [][]byte
	i = skipSpace(data, i+1)
	if i < len(data) && data[i] == ']' {
		return elems, skipSpace(data, i+1) == len(data)
	}
	for {
		end := valueEnd(data, i, 2)
		if end < 0 {
			return nil, false
		}
		elems = append(elems, data[i:end:end])
		i = skipSpace(data, end)
		if i >= len(data) {
			return nil, false
		}
		switch data[i] {
		case ',':
			i = skipSpace(data, i+1)
		case ']':
			if skipSpace(data, i+1) != len(data) {
				return nil, false
			}
			return elems, true
		default:
			return nil, false
		}
	}
}

// members returns the members of data, a JSON object known to be valid (one a check of the
// text holding it accepted), without checking it again. On other text it returns some slices
// of data, or nil; it does not panic.
func members(data []byte) []Member {
	i := skipSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return nil
	}
	var ms []Member
	for i = skipSpace(data, i+1); i < len(data) && data[i] == '"'; {
		kEnd := skipString(data, i)
		key := data[i:kEnd:kEnd]
		i = skipSpace(data, kEnd)
		if i >= len(data) || data[i] != ':' {
			return ms
		}
		i = skipSpace(data, i+1)
		vEnd := skip(data, i)
		ms = append(ms, Member{Key: key, Value: data[i:vEnd:vEnd]})
		if i = skipSpace(data, vEnd); i >= len(data) || data[i] != ',' {
			return ms
		}
		i = skipSpace(data, i+1)
	}
	return ms
}

// elements returns the elements of data, a JSON array known to be valid, without checking it
// again; on other text, as members.
func elements(data []byte) [][]byte {
	i := skipSpace(data, 0)
	if i >= len(data) || data[i] != '[' {
		return nil
	}
	var es [][]byte
	i = skipSpace(data, i+1)
	if i < len(data) && data[i] == ']' {
		return [][]byte{}
	}
	for i < len(data) {
		end := skip(data, i)
		if end <= i {
			return es
		}
		es = append(es, data[i:end:end])
		if i = skipSpace(data, end); i >= len(data) || data[i] != ',' {
			return es
		}
		i = skipSpace(data, i+1)
	}
	return es
}

// skip returns the index just after the value at data[i] in text known to be valid, finding
// the end of each string with bytes.IndexByte.
func skip(data []byte, i int) int {
	depth := 0
	for i < len(data) {
		switch data[i] {
		case '"':
			i = skipString(data, i)
		case '{', '[':
			depth++
			i++
		case '}', ']':
			depth--
			i++
		default:
			i++
			if depth == 0 {
				// A number or literal ends at the first byte that cannot be part of it.
				for i < len(data) && !isSpace(data[i]) && data[i] != ',' && data[i] != '}' && data[i] != ']' {
					i++
				}
				return i
			}
			continue
		}
		if depth <= 0 {
			return i
		}
	}
	return len(data)
}

// skipString returns the index just after the string that starts at data[i] in text known to be
// valid: after the first quote not escaped by an odd run of backslashes.
func skipString(data []byte, i int) int {
	for i++; i < len(data); {
		j := bytes.IndexByte(data[i:], '"')
		if j < 0 {
			return len(data)
		}
		q := i + j
		n := 0
		for k := q - 1; k >= i && data[k] == '\\'; k-- {
			n++
		}
		if n%2 == 0 {
			return q + 1
		}
		i = q + 1
	}
	return len(data)
}

// AppendCompact appends to dst the JSON text src without the whitespace outside its strings, as
// json.Compact writes it (and json.Marshal does a json.RawMessage, with HTML escaping off), and
// reports whether src is one valid JSON value as Valid does. For text that is not, it appends
// nothing and returns dst.
func AppendCompact(dst, src []byte) ([]byte, bool) {
	if !Valid(src) {
		return dst, false
	}
	start := 0
	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"':
			i = skipString(src, i)
		case isSpace(c):
			dst = append(dst, src[start:i]...)
			i++
			start = i
		default:
			i++
		}
	}
	return append(dst, src[start:]...), true
}

// Trim returns data without the JSON whitespace around it.
func Trim(data []byte) []byte {
	i := skipSpace(data, 0)
	j := len(data)
	for j > i && isSpace(data[j-1]) {
		j--
	}
	return data[i:j:j]
}

// Unquote returns the text of the valid JSON string literal s, quotes included, as
// json.Unmarshal decodes it. ok is false, and the text is left to encoding/json, when s holds
// invalid UTF-8 or a \u escape of a UTF-16 surrogate that is not half of a pair, which
// encoding/json replaces.
func Unquote(s []byte) (text string, ok bool) {
	if len(s) < 2 {
		return "", false
	}
	s = s[1 : len(s)-1]
	i := bytes.IndexByte(s, '\\')
	if !utf8.Valid(s) {
		return "", false
	}
	if i < 0 {
		return string(s), true
	}
	var b strings.Builder
	b.Grow(len(s))
	for i >= 0 {
		b.Write(s[:i])
		if i+1 >= len(s) {
			return "", false
		}
		switch c := s[i+1]; c {
		case '"', '\\', '/':
			b.WriteByte(c)
			s = s[i+2:]
		case 'b':
			b.WriteByte('\b')
			s = s[i+2:]
		case 'f':
			b.WriteByte('\f')
			s = s[i+2:]
		case 'n':
			b.WriteByte('\n')
			s = s[i+2:]
		case 'r':
			b.WriteByte('\r')
			s = s[i+2:]
		case 't':
			b.WriteByte('\t')
			s = s[i+2:]
		case 'u':
			if i+6 > len(s) {
				return "", false
			}
			r := rune(0)
			for _, h := range s[i+2 : i+6] {
				d, ok := hexDigit(h)
				if !ok {
					return "", false
				}
				r = r<<4 | d
			}
			s = s[i+6:]
			if utf16.IsSurrogate(r) {
				lo, ok := hex4(s)
				pair := utf16.DecodeRune(r, lo)
				if !ok || pair == utf8.RuneError {
					return "", false
				}
				r = pair
				s = s[6:]
			}
			b.WriteRune(r)
		default:
			return "", false
		}
		i = bytes.IndexByte(s, '\\')
	}
	b.Write(s)
	return b.String(), true
}

// IsString reports whether the valid JSON value v is a string.
func IsString(v []byte) bool { return len(v) > 0 && v[0] == '"' }

// IsNull reports whether the valid JSON value v is null.
func IsNull(v []byte) bool { return len(v) > 0 && v[0] == 'n' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipSpace(data []byte, i int) int {
	for i < len(data) && isSpace(data[i]) {
		i++
	}
	return i
}

// valueEnd returns the index just after the JSON value that starts at data[i], or -1 when no
// valid value starts there. depth is the nesting depth an array or object starting there has.
func valueEnd(data []byte, i, depth int) int {
	if i >= len(data) {
		return -1
	}
	switch c := data[i]; {
	case c == '"':
		return stringEnd(data, i)
	case c == '{':
		if depth > maxDepth {
			return -1
		}
		i = skipSpace(data, i+1)
		if i < len(data) && data[i] == '}' {
			return i + 1
		}
		for {
			if i >= len(data) || data[i] != '"' {
				return -1
			}
			if i = stringEnd(data, i); i < 0 {
				return -1
			}
			i = skipSpace(data, i)
			if i >= len(data) || data[i] != ':' {
				return -1
			}
			if i = valueEnd(data, skipSpace(data, i+1), depth+1); i < 0 {
				return -1
			}
			i = skipSpace(data, i)
			if i >= len(data) {
				return -1
			}
			switch data[i] {
			case ',':
				i = skipSpace(data, i+1)
			case '}':
				return i + 1
			default:
				return -1
			}
		}
	case c == '[':
		if depth > maxDepth {
			return -1
		}
		i = skipSpace(data, i+1)
		if i < len(data) && data[i] == ']' {
			return i + 1
		}
		for {
			if i = valueEnd(data, i, depth+1); i < 0 {
				return -1
			}
			i = skipSpace(data, i)
			if i >= len(data) {
				return -1
			}
			switch data[i] {
			case ',':
				i = skipSpace(data, i+1)
			case ']':
				return i + 1
			default:
				return -1
			}
		}
	case c == 't':
		return literalEnd(data, i, "true")
	case c == 'f':
		return literalEnd(data, i, "false")
	case c == 'n':
		return literalEnd(data, i, "null")
	case c == '-' || '0' <= c && c <= '9':
		return numberEnd(data, i)
	}
	return -1
}

func literalEnd(data []byte, i int, lit string) int {
	if len(data)-i < len(lit) || string(data[i:i+len(lit)]) != lit {
		return -1
	}
	return i + len(lit)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// numberEnd returns the index just after the JSON number at data[i], or -1.
func numberEnd(data []byte, i int) int {
	if data[i] == '-' {
		i++
	}
	switch {
	case i >= len(data):
		return -1
	case data[i] == '0':
		i++
	case isDigit(data[i]):
		for i++; i < len(data) && isDigit(data[i]); i++ {
		}
	default:
		return -1
	}
	if i < len(data) && data[i] == '.' {
		i++
		if i >= len(data) || !isDigit(data[i]) {
			return -1
		}
		for i++; i < len(data) && isDigit(data[i]); i++ {
		}
	}
	if i < len(data) && (data[i] == 'e' || data[i] == 'E') {
		i++
		if i < len(data) && (data[i] == '+' || data[i] == '-') {
			i++
		}
		if i >= len(data) || !isDigit(data[i]) {
			return -1
		}
		for i++; i < len(data) && isDigit(data[i]); i++ {
		}
	}
	return i
}

// plain marks the bytes a JSON string holds as they are: not a quote, a backslash or a control
// character.
var plain = func() (t [256]bool) {
	for c := 0x20; c < 256; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

// stringEnd returns the index just after the JSON string that starts at data[i] (a quote), or
// -1 when it is not a valid string.
func stringEnd(data []byte, i int) int {
	i++
	for {
		i = plainEnd(data, i)
		if i >= len(data) {
			return -1
		}
		switch data[i] {
		case '"':
			return i + 1
		case '\\':
			if i+1 >= len(data) {
				return -1
			}
			switch data[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(data) {
					return -1
				}
				for _, h := range data[i+2 : i+6] {
					if _, ok := hexDigit(h); !ok {
						return -1
					}
				}
				i += 6
			default:
				return -1
			}
		default:
			return -1 // a control character
		}
	}
}

// Eight bytes of each value, for testing eight bytes at once.
const (
	ones   = 0x0101010101010101
	highs  = 0x8080808080808080
	quotes = '"' * ones
	slashs = '\\' * ones
	spaces = 0x20 * ones
)

// plainEnd returns the index of the first byte at or after data[i] that a JSON string does not
// hold as it is (see plain), or len(data). It tests eight bytes at a time: a byte b is zero in
// x^quotes when it is a quote, and (v-ones)&^v has the high bit of the lowest zero byte of v set
// (and none below it); (x-spaces)&^x does the same for the lowest byte under 0x20.
func plainEnd(data []byte, i int) int {
	// Thirty-two bytes at a time while none is special, then eight to find which.
	for ; i+32 <= len(data); i += 32 {
		w := data[i : i+32 : i+32]
		x0 := binary.LittleEndian.Uint64(w[0:8])
		x1 := binary.LittleEndian.Uint64(w[8:16])
		x2 := binary.LittleEndian.Uint64(w[16:24])
		x3 := binary.LittleEndian.Uint64(w[24:32])
		if (special(x0)|special(x1)|special(x2)|special(x3))&highs != 0 {
			break
		}
	}
	for ; i+8 <= len(data); i += 8 {
		x := binary.LittleEndian.Uint64(data[i:])
		if t := special(x) & highs; t != 0 {
			return i + bits.TrailingZeros64(t)/8
		}
	}
	for i < len(data) && plain[data[i]] {
		i++
	}
	return i
}

// special has the high bit set in the lowest byte of x that is a quote, a backslash or under
// 0x20 (and maybe in higher bytes), and in none if there is none.
func special(x uint64) uint64 {
	q, s := x^quotes, x^slashs
	return ((q - ones) &^ q) | ((s - ones) &^ s) | ((x - spaces) &^ x)
}

// hex4 reads the \u escape at the start of s.
func hex4(s []byte) (rune, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return 0, false
	}
	r := rune(0)
	for _, h := range s[2:6] {
		d, ok := hexDigit(h)
		if !ok {
			return 0, false
		}
		r = r<<4 | d
	}
	return r, true
}

func hexDigit(c byte) (rune, bool) {
	switch {
	case '0' <= c && c <= '9':
		return rune(c - '0'), true
	case 'a' <= c && c <= 'f':
		return rune(c - 'a' + 10), true
	case 'A' <= c && c <= 'F':
		return rune(c - 'A' + 10), true
	}
	return 0, false
}
