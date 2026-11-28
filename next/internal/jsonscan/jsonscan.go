package jsonscan

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxDepth = 10000

func Valid(data []byte) bool {
	i := skipSpace(data, 0)
	end := valueEnd(data, i, 1)
	return end >= 0 && skipSpace(data, end) == len(data)
}

type Member struct {
	Key, Value []byte
}

func Object(data []byte) ([]Member, bool) { return AppendObject(nil, data) }

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

func Trim(data []byte) []byte {
	i := skipSpace(data, 0)
	j := len(data)
	for j > i && isSpace(data[j-1]) {
		j--
	}
	return data[i:j:j]
}

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

func IsString(v []byte) bool { return len(v) > 0 && v[0] == '"' }

func IsNull(v []byte) bool { return len(v) > 0 && v[0] == 'n' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipSpace(data []byte, i int) int {
	for i < len(data) && isSpace(data[i]) {
		i++
	}
	return i
}

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

var plain = func() (t [256]bool) {
	for c := 0x20; c < 256; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

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
			return -1
		}
	}
}

const (
	ones   = 0x0101010101010101
	highs  = 0x8080808080808080
	quotes = '"' * ones
	slashs = '\\' * ones
	spaces = 0x20 * ones
)

func plainEnd(data []byte, i int) int {
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

func special(x uint64) uint64 {
	q, s := x^quotes, x^slashs
	return ((q - ones) &^ q) | ((s - ones) &^ s) | ((x - spaces) &^ x)
}

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
