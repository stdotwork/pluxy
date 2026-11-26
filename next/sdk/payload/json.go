package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// The body is edited as bytes: a write cuts the old value's span out and puts the new text in,
// so everything it does not name keeps its bytes (key order, spacing, number formatting). The
// functions here assume valid JSON whose root is an object or an array; Apply checks that once.

// maxPad bounds the nulls a write may add to reach an array index.
const maxPad = 1 << 24

var (
	errMalformed  = errors.New("malformed JSON")
	errNonNumeric = errors.New("cannot set an array element by a non-numeric key")
	errHugeIndex  = errors.New("array index too large")
)

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// stringEnd returns the index after the string that starts at b[i], or -1.
func stringEnd(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return -1
}

// valueEnd returns the index after the value that starts at b[i], or -1.
func valueEnd(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return stringEnd(b, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				e := stringEnd(b, j)
				if e < 0 {
					return -1
				}
				j = e - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	}
	j := i
	for j < len(b) {
		switch b[j] {
		case ',', ']', '}', ' ', '\t', '\n', '\r':
			if j == i {
				return -1
			}
			return j
		}
		j++
	}
	return j
}

// nextMember reads the object member that starts at or after b[j]: its key span ks..ke (quotes
// included) and the start of its value.
func nextMember(b []byte, j int) (ks, ke, vs int, ok bool) {
	ks = skipSpace(b, j)
	if ks >= len(b) || b[ks] != '"' {
		return 0, 0, 0, false
	}
	if ke = stringEnd(b, ks); ke < 0 {
		return 0, 0, 0, false
	}
	k := skipSpace(b, ke)
	if k >= len(b) || b[k] != ':' {
		return 0, 0, 0, false
	}
	if vs = skipSpace(b, k+1); vs >= len(b) {
		return 0, 0, 0, false
	}
	return ks, ke, vs, true
}

// keyIs reports whether the key at b[ks:ke] (quotes included) is key once unescaped.
func keyIs(b []byte, ks, ke int, key string) bool {
	raw := b[ks+1 : ke-1]
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw) == key
	}
	var s string
	return json.Unmarshal(b[ks:ke], &s) == nil && s == key
}

// findKey looks for key directly inside the object at b[i]. It returns the value span when
// found. Otherwise closeIdx is the index of the closing brace, or -1 when the object is
// malformed, and empty reports whether the object has no members.
func findKey(b []byte, i int, key string) (vs, ve, closeIdx int, empty bool) {
	j := i + 1
	empty = true
	for {
		if k := skipSpace(b, j); k < len(b) && b[k] == '}' {
			return -1, -1, k, empty
		}
		ks, ke, s, ok := nextMember(b, j)
		if !ok {
			return -1, -1, -1, empty
		}
		empty = false
		e := valueEnd(b, s)
		if e < 0 {
			return -1, -1, -1, false
		}
		if keyIs(b, ks, ke, key) {
			return s, e, 0, false
		}
		j = skipSpace(b, e)
		if j < len(b) && b[j] == ',' {
			j++
			continue
		}
		if j < len(b) && b[j] == '}' {
			return -1, -1, j, false
		}
		return -1, -1, -1, false
	}
}

type arrayScan struct {
	n      int // elements seen: all of them unless the target was found
	vs, ve int // span of the target element
	close  int // index of ']'
	found  bool
	bad    bool
}

// scanArray walks the array at b[i], stopping at element idx when it exists. A large idx counts
// every element.
func scanArray(b []byte, i, idx int) (a arrayScan) {
	j := skipSpace(b, i+1)
	if j < len(b) && b[j] == ']' {
		a.close = j
		return a
	}
	for {
		j = skipSpace(b, j)
		e := valueEnd(b, j)
		if e < 0 {
			a.bad = true
			return a
		}
		if a.n == idx {
			a.vs, a.ve, a.found = j, e, true
			a.n++
			return a
		}
		a.n++
		j = skipSpace(b, e)
		if j < len(b) && b[j] == ',' {
			j++
			continue
		}
		if j < len(b) && b[j] == ']' {
			a.close = j
			return a
		}
		a.bad = true
		return a
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// arrayIndex reads an all-digit path component, capping what does not fit an int.
func arrayIndex(s string) (int, bool) {
	if !isDigits(s) {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if n > (math.MaxInt-9)/10 {
			return math.MaxInt, true
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// child returns the span of the member or element c of the container at b[i], or -1.
func child(b []byte, i int, c string) (vs, ve int) {
	switch b[i] {
	case '{':
		vs, ve, _, _ = findKey(b, i, c)
		return vs, ve
	case '[':
		if idx, ok := arrayIndex(c); ok {
			if a := scanArray(b, i, idx); a.found {
				return a.vs, a.ve
			}
		}
	}
	return -1, -1
}

// value is a value found in a document: its raw text.
type value struct {
	raw []byte
	ok  bool
}

// null reports whether the value is JSON null.
func (v value) null() bool { return string(v.raw) == "null" }

// lookup finds the value at the plain path comps. A path through a scalar finds nothing.
func lookup(b []byte, comps []string) value {
	i := skipSpace(b, 0)
	if i >= len(b) || len(comps) == 0 {
		return value{}
	}
	for _, c := range comps {
		if b[i] != '{' && b[i] != '[' {
			return value{}
		}
		vs, _ := child(b, i, c)
		if vs < 0 {
			return value{}
		}
		i = vs
	}
	e := valueEnd(b, i)
	if e < 0 {
		return value{}
	}
	return value{raw: b[i:e], ok: true}
}

// elements returns the raw elements of the array raw.
func elements(raw []byte) [][]byte {
	var out [][]byte
	j := skipSpace(raw, 1)
	for j < len(raw) && raw[j] != ']' {
		e := valueEnd(raw, j)
		if e < 0 {
			break
		}
		out = append(out, raw[j:e])
		if j = skipSpace(raw, e); j < len(raw) && raw[j] == ',' {
			j = skipSpace(raw, j+1)
		}
	}
	return out
}

func splice(b []byte, lo, hi int, ins ...[]byte) []byte {
	n := lo + len(b) - hi
	for _, p := range ins {
		n += len(p)
	}
	out := make([]byte, 0, n)
	out = append(out, b[:lo]...)
	for _, p := range ins {
		out = append(out, p...)
	}
	return append(out, b[hi:]...)
}

// quote writes s as a JSON string the way sjson does: printable ASCII without a quote or a
// backslash goes in as it is (so `<`, `>` and `&` stay unescaped), and any other string goes
// through encoding/json.
func quote(s string) []byte {
	plain := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x80 || c == '"' || c == '\\' {
			plain = false
			break
		}
	}
	if plain {
		return append(append([]byte{'"'}, s...), '"')
	}
	out, _ := json.Marshal(s) // a string always encodes
	return out
}

// build returns the text that goes before and after a value to create the missing components:
// an all-digit component makes an array padded with nulls up to it, any other an object.
func build(comps []string) (pre, post []byte, err error) {
	var closers []byte
	for _, c := range comps {
		if n, ok := arrayIndex(c); ok {
			if n > maxPad {
				return nil, nil, errHugeIndex
			}
			pre = append(pre, '[')
			for k := 0; k < n; k++ {
				pre = append(pre, "null,"...)
			}
			closers = append(closers, ']')
			continue
		}
		pre = append(append(append(pre, '{'), quote(c)...), ':')
		closers = append(closers, '}')
	}
	for l, r := 0, len(closers)-1; l < r; l, r = l+1, r-1 {
		closers[l], closers[r] = closers[r], closers[l]
	}
	return pre, closers, nil
}

// setValue sets the value at comps to the raw JSON val, creating what is missing. It writes the
// way sjson does: a new object member goes before the closing brace, and an array grown past
// its end is rewritten without whitespace and padded with nulls.
func setValue(b []byte, comps []string, val []byte) ([]byte, error) {
	i := skipSpace(b, 0)
	root := true
	for {
		c := comps[0]
		var vs, ve int
		if b[i] == '{' {
			var closeIdx int
			var empty bool
			vs, ve, closeIdx, empty = findKey(b, i, c)
			if vs < 0 {
				if closeIdx < 0 {
					return nil, errMalformed
				}
				pre, post, err := build(comps[1:])
				if err != nil {
					return nil, err
				}
				var sep []byte
				if !empty {
					sep = []byte{','}
				}
				ins := [][]byte{sep, quote(c), {':'}, pre, val, post}
				if root {
					from, to := skipSpace(b, 0), len(bytes.TrimRight(b, " \t\n\r"))
					return splice(b[from:to], closeIdx-from, closeIdx-from, ins...), nil
				}
				return splice(b, closeIdx, closeIdx, ins...), nil
			}
		} else {
			if !isDigits(c) {
				return nil, errNonNumeric
			}
			idx, _ := arrayIndex(c)
			a := scanArray(b, i, idx)
			if a.bad {
				return nil, errMalformed
			}
			if !a.found {
				pre, post, err := build(comps[1:])
				if err != nil {
					return nil, err
				}
				pad := max(idx-a.n, 0)
				if pad > maxPad {
					return nil, errHugeIndex
				}
				head := []byte{'['}
				j := i + 1
				for k := 0; k < a.n; k++ {
					j = skipSpace(b, j)
					e := valueEnd(b, j)
					head = append(append(head, b[j:e]...), ',')
					j = skipSpace(b, e) + 1
				}
				for k := 0; k < pad; k++ {
					head = append(head, "null,"...)
				}
				lo, hi := i, a.close+1
				if root {
					lo, hi = 0, len(b)
				}
				return splice(b, lo, hi, head, pre, val, post, []byte{']'}), nil
			}
			vs, ve = a.vs, a.ve
		}
		if len(comps) == 1 {
			return splice(b, vs, ve, val), nil
		}
		if b[vs] == '{' || b[vs] == '[' {
			i, comps, root = vs, comps[1:], false
			continue
		}
		pre, post, err := build(comps[1:])
		if err != nil {
			return nil, err
		}
		return splice(b, vs, ve, pre, val, post), nil
	}
}

// deleteValue removes the value at comps, with sjson's handling of commas: a later member goes
// together with the comma before it, a first one with the comma after it. It reports whether
// the value existed.
func deleteValue(b []byte, comps []string) ([]byte, bool) {
	i := skipSpace(b, 0)
	for ; len(comps) > 1; comps = comps[1:] {
		vs, _ := child(b, i, comps[0])
		if vs < 0 || (b[vs] != '{' && b[vs] != '[') {
			return b, false
		}
		i = vs
	}
	c := comps[0]
	obj := b[i] == '{'
	idx := 0
	if !obj {
		n, ok := arrayIndex(c)
		if !ok {
			return b, false
		}
		idx = n
	}
	j, comma := i+1, -1
	for k := 0; ; k++ {
		start, vs, match := j, 0, false
		if obj {
			ks, ke, v, ok := nextMember(b, j)
			if !ok {
				return b, false
			}
			vs, match = v, keyIs(b, ks, ke, c)
		} else {
			vs = skipSpace(b, j)
			if vs >= len(b) || b[vs] == ']' {
				return b, false
			}
			start, match = vs, k == idx
		}
		ve := valueEnd(b, vs)
		if ve < 0 {
			return b, false
		}
		next := skipSpace(b, ve)
		if match {
			switch {
			case comma >= 0:
				return splice(b, comma, ve), true
			case next < len(b) && b[next] == ',':
				return splice(b, start, next+1), true
			}
			return splice(b, start, ve), true
		}
		if next >= len(b) || b[next] != ',' {
			return b, false
		}
		comma, j = next, next+1
	}
}

// encodeValue returns the JSON text of a configured value: strings are written by quote, numbers
// without an exponent, and anything else through encoding/json.
func encodeValue(v any) ([]byte, error) {
	switch v := v.(type) {
	case string:
		return quote(v), nil
	case []byte:
		return quote(string(v)), nil
	case nil:
		return []byte("null"), nil
	case bool:
		return strconv.AppendBool(nil, v), nil
	case int:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int8:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int16:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int32:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int64:
		return strconv.AppendInt(nil, v, 10), nil
	case uint:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint8:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint16:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint32:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint64:
		return strconv.AppendUint(nil, v, 10), nil
	case float32:
		return appendFloat(float64(v))
	case float64:
		return appendFloat(v)
	}
	return json.Marshal(v)
}

func appendFloat(f float64) ([]byte, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("%v is not a JSON number", f)
	}
	return strconv.AppendFloat(nil, f, 'f', -1, 64), nil
}
