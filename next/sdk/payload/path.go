package payload

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// A rule path is a list of dotted parts. A part is a key, an array index (all digits) or a
// query `#(...)` that picks the first matching element of the array before it, or every
// matching element with `#(...)#`. These are the parts the old rule paths use; the rest of
// their gjson/sjson syntax (wildcards, `|`, `@`, `-1` to append, `#` counts, multipaths) is
// refused by Validate.
//
// A key may hold a dot written as `\.`; that is the only escape. A query is one or more terms
// joined by `&&` and `||` (`&&` binds tighter), each `key==value` or `key!=value`, where key is
// a dotted path of plain keys and value is a JSON string, number, `true` or `false`.

type part struct {
	key   string
	query [][]term // groups joined by ||; each group is terms joined by &&. nil for a key.
	all   bool     // `#(...)#`: every match, not the first
}

type term struct {
	path  []string
	equal bool // == (true) or != (false)
	val   string
}

// parsePath reads a rule path. The error says what is outside the supported syntax.
func parsePath(path string) ([]part, error) {
	if path == "" {
		return nil, fmt.Errorf("the path is empty")
	}
	var parts []part
	for i := 0; ; {
		var p part
		var next int
		var err error
		if strings.HasPrefix(path[i:], "#(") {
			p, next, err = parseQueryPart(path, i)
		} else {
			p, next, err = parseKeyPart(path, i)
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
		if next >= len(path) {
			return parts, nil
		}
		i = next + 1 // the dot
	}
}

// parseKeyPart reads a key or index that starts at path[i] and ends at an unescaped dot.
func parseKeyPart(path string, i int) (part, int, error) {
	var key []byte
	j := i
	for ; j < len(path); j++ {
		ch := path[j]
		if ch == '.' {
			break
		}
		if ch == '\\' {
			if j+1 >= len(path) || path[j+1] != '.' {
				return part{}, 0, fmt.Errorf(`a backslash may only escape a dot (\.)`)
			}
			j++
			key = append(key, '.')
			continue
		}
		if strings.IndexByte(`*?|@#"'()`, ch) >= 0 {
			return part{}, 0, fmt.Errorf("%q is not supported in a key (wildcards, pipes, modifiers, counts and quotes are not part of the supported syntax)", ch)
		}
		key = append(key, ch)
	}
	k := string(key)
	switch {
	case k == "":
		return part{}, 0, fmt.Errorf("a part of the path is empty")
	case k == "-1":
		return part{}, 0, fmt.Errorf("-1 (append) is not supported; write the index")
	case strings.IndexByte(`:[{!`, k[0]) >= 0:
		return part{}, 0, fmt.Errorf("a key may not start with %q", k[0])
	case isDigits(k):
		if n, _ := arrayIndex(k); (len(k) > 1 && k[0] == '0') || n > maxPad {
			return part{}, 0, fmt.Errorf("index %s is not supported (no leading zeros, at most %d)", k, maxPad)
		}
	}
	return part{key: k}, j, nil
}

// parseQueryPart reads `#(...)` or `#(...)#` that starts at path[i].
func parseQueryPart(path string, i int) (part, int, error) {
	closeIdx := queryClose(path, i)
	if closeIdx < 0 {
		return part{}, 0, fmt.Errorf("a query is not closed")
	}
	next := closeIdx + 1
	all := false
	if next < len(path) && path[next] == '#' {
		all = true
		next++
	}
	if next < len(path) && path[next] != '.' {
		return part{}, 0, fmt.Errorf("a query must end its part (only `#` may follow it)")
	}
	groups, err := parseQuery(path[i+2 : closeIdx])
	if err != nil {
		return part{}, 0, err
	}
	return part{query: groups, all: all}, next, nil
}

// queryClose returns the index of the parenthesis that closes the `#(` at path[i], or -1.
func queryClose(path string, i int) int {
	depth := 1
	var quote byte
	for j := i + 2; j < len(path); j++ {
		ch := path[j]
		switch {
		case ch == '\\':
			j++
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '(':
			depth++
		case ch == ')':
			if depth--; depth == 0 {
				return j
			}
		}
	}
	return -1
}

// splitLogical splits query at op, outside quotes.
func splitLogical(query, op string) []string {
	var out []string
	start := 0
	var quote byte
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case ch == '\\':
			i++
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case strings.HasPrefix(query[i:], op):
			out = append(out, strings.TrimSpace(query[start:i]))
			i += len(op) - 1
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(query[start:]))
}

func parseQuery(q string) ([][]term, error) {
	var groups [][]term
	for _, or := range splitLogical(q, "||") {
		var group []term
		for _, s := range splitLogical(or, "&&") {
			t, err := parseTerm(s)
			if err != nil {
				return nil, fmt.Errorf("query term %q: %w", s, err)
			}
			group = append(group, t)
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func parseTerm(s string) (term, error) {
	op := strings.IndexAny(s, "=!<>%")
	if op < 0 {
		return term{}, fmt.Errorf("it needs == or !=")
	}
	var t term
	switch {
	case strings.HasPrefix(s[op:], "=="):
		t.equal = true
	case strings.HasPrefix(s[op:], "!="):
	default:
		return term{}, fmt.Errorf("only == and != are supported")
	}
	for _, k := range strings.Split(strings.TrimSpace(s[:op]), ".") {
		if k == "" || strings.IndexFunc(k, func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '$' || r == ' ')
		}) >= 0 {
			return term{}, fmt.Errorf("its key must be plain keys (letters, digits, space, _, -, $) joined by dots")
		}
		if isDigits(k) && len(k) > 1 && k[0] == '0' {
			return term{}, fmt.Errorf("an index may not have leading zeros")
		}
		t.path = append(t.path, k)
	}
	lit := strings.TrimSpace(s[op+2:])
	switch {
	case strings.HasPrefix(lit, `"`):
		if err := json.Unmarshal([]byte(lit), &t.val); err != nil {
			return term{}, fmt.Errorf("its value is not a JSON string")
		}
	case lit == "true" || lit == "false":
		t.val = lit
	case lit != "" && (lit[0] == '-' || lit[0] >= '0' && lit[0] <= '9') && json.Valid([]byte(lit)):
		t.val = lit
	default:
		return term{}, fmt.Errorf("its value must be a JSON string, number, true or false")
	}
	return t, nil
}

// matches reports whether the array element item satisfies the query, as gjson's `#(...)` does.
func (p part) matches(item []byte) bool {
	for _, group := range p.query {
		ok := true
		for _, t := range group {
			if !t.matches(item) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// matches compares the value at the term's path with the term's value the way gjson does: a
// string compares as text, a number as a number (the value is read as one, and as 0 when it is
// not), true and false as the words, and null, objects and arrays never match.
func (t term) matches(item []byte) bool {
	v := lookup(item, t.path)
	if !v.ok {
		return false
	}
	switch v.raw[0] {
	case '"':
		var s string
		if json.Unmarshal(v.raw, &s) != nil {
			return false
		}
		return (s == t.val) == t.equal
	case 't':
		return (t.val == "true") == t.equal
	case 'f':
		return (t.val == "false") == t.equal
	case 'n', '{', '[':
		return false
	}
	num, _ := strconv.ParseFloat(string(v.raw), 64)
	want, _ := strconv.ParseFloat(t.val, 64)
	return (num == want) == t.equal
}
