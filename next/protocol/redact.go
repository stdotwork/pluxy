package protocol

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/stdotwork/pluxy/next/llm"
)

const Redacted = "[redacted]"

// Redactor removes one call's credential from what the call sends back. A nil Redactor changes
// nothing.
type Redactor struct {
	r       *strings.Replacer
	find    *strings.Replacer
	forms   []string
	min     int
	chars   [256]bool
	numeric bool
	fffd    bool
}

// NewRedactor returns the Redactor for the credentials cs. Every string in a credential except its
// kind counts as secret.
func NewRedactor(cs ...*Credential) *Redactor {
	var secrets []string
	for _, c := range cs {
		if c == nil {
			continue
		}
		b, _ := json.Marshal(c)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		delete(m, "kind")
		secrets = collectStrings(m, secrets)
	}
	for _, s := range secrets {
		if strings.Contains(s, "\n") {
			for _, line := range strings.Split(s, "\n") {
				if line = strings.TrimSpace(line); len(line) >= minFragment {
					secrets = append(secrets, line)
				}
			}
		}
	}
	for _, s := range secrets {
		secrets = append(secrets, SecretParts(s, minFragment)...)
	}
	var variants []string
	for _, s := range secrets {
		variants = append(variants, encodings(s)...)
	}
	if len(variants) == 0 {
		return &Redactor{}
	}
	slices.SortFunc(variants, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	variants = slices.Compact(variants)
	red := &Redactor{forms: variants, min: len(variants[len(variants)-1])}
	pairs := make([]string, 0, 2*len(variants))
	gone := make([]string, 0, 2*len(variants))
	for _, v := range variants {
		pairs = append(pairs, v, Redacted)
		gone = append(gone, v, "")
		numeric := true
		for i := range len(v) {
			red.chars[v[i]] = true
			numeric = numeric && strings.IndexByte(numberChars, v[i]) >= 0
		}
		red.numeric = red.numeric || numeric
		red.fffd = red.fffd || strings.ContainsRune(v, utf8.RuneError)
	}
	red.r, red.find = strings.NewReplacer(pairs...), strings.NewReplacer(gone...)
	return red
}

// SecretParts returns the parts of the secret s that are secret on their own, each at least min
// bytes long, in the order found; none when s has none of the structures below. These structures
// are split, and each part split again, up to 4 times; of what is still left to split then, the
// last word and the parts nothing splits further are parts too, so a token behind more schemes or
// prefixes than that is still found:
//
//   - an authorization value, "Bearer <token>" or "Basic <base64>" (the scheme in any letter case):
//     the token, or the base64 text (standard or URL-safe, padded or not), what it decodes to when
//     that is text or holds a ":" ("user:password", in any encoding) and the password, or
//     the user name when the password is empty, as a key sent as Basic's user name is;
//   - a token behind a public prefix, "<prefix>$<token>" or "<prefix>:<token>" (such as Devin's
//     "devin-session-token$<jwt>", Meta's "dca:<token>" or a header line "Name: value"), where
//     the prefix is a letter and up to 31 more letters, digits, "-", "_" or ".", and the token
//     (spaces and tabs after the "$" or ":" aside) does not start with "/", so a URL's
//     "scheme://" is not one: the token;
//   - a cookie list, "name=value; name=value" (or one pair): each value, its double quotes
//     aside, where the name is an HTTP token and the value holds only cookie characters. A value
//     that is empty or only "=" characters, its quotes aside, adds no part: after a key's first
//     "=" that is base64 padding, so "abc==" is no pair, while "abc=def==" still is.
//
// A prefix, a scheme or a cookie name is never a part.
func SecretParts(s string, min int) []string {
	var out []string
	seen := map[string]bool{s: true}
	add := func(p string) bool {
		if len(p) < min || len(p) == 0 || seen[p] {
			return false
		}
		seen[p] = true
		out = append(out, p)
		return true
	}
	var split func(string, int)
	split = func(s string, depth int) {
		shape := directParts(s)
		if depth == maxPartDepth {
			if len(shape.parts) > 0 {
				add(lastWord(s))
				for _, p := range shape.leafParts() {
					add(p)
				}
			}
			return
		}
		for _, p := range shape.parts {
			if add(p.text) {
				split(p.text, depth+1)
			}
		}
	}
	split(s, 0)
	return out
}

const maxPartDepth = 4

const maxPrefix = 32

type partKind int

const (
	partWrapped partKind = iota
	partValue
	partForm
)

type secretPart struct {
	text string
	kind partKind
}

type secretShape struct {
	parts []secretPart
	pair  string
	mixed bool
}

func directParts(s string) secretShape {
	s = strings.TrimSpace(s)
	var shape secretShape
	wrapped := false
	if i := strings.IndexAny(s, " \t"); i > 0 {
		scheme, rest := s[:i], strings.TrimLeft(s[i:], " \t")
		switch {
		case rest == "":
		case strings.EqualFold(scheme, "Bearer"):
			wrapped = true
			shape.parts = append(shape.parts, secretPart{rest, partWrapped})
		case strings.EqualFold(scheme, "Basic"):
			wrapped = true
			shape.parts = append(append(shape.parts, secretPart{rest, partWrapped}), basicParts(rest)...)
		}
	}
	if i, ok := publicPrefixEnd(s); ok {
		if rest := strings.TrimLeft(s[i+1:], " \t"); rest != "" && !strings.HasPrefix(rest, "/") {
			wrapped = true
			shape.parts = append(shape.parts, secretPart{rest, partWrapped})
		}
	}
	if strings.IndexByte(s, '=') >= 0 {
		pairs, other, pair := 0, false, ""
		for _, c := range strings.Split(s, ";") {
			if value, ok := cookiePair(c); ok {
				pairs++
				pair = strings.TrimSpace(c)
				shape.parts = append(shape.parts, secretPart{value, partValue})
			} else if strings.TrimSpace(c) != "" {
				other = true
			}
		}
		if !wrapped && pairs == 1 && !other {
			shape.pair = pair
		}
		shape.mixed = !wrapped && pairs > 0 && other
	}
	return shape
}

func basicParts(text string) []secretPart {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		b, err := enc.DecodeString(text)
		if err != nil {
			continue
		}
		decoded := string(b)
		user, pass, ok := strings.Cut(decoded, ":")
		if !ok && !utf8.Valid(b) {
			continue
		}
		switch {
		case !ok:
			return []secretPart{{decoded, partWrapped}}
		case pass == "":
			return []secretPart{{decoded, partForm}, {user, partValue}}
		}
		return []secretPart{{decoded, partForm}, {pass, partValue}}
	}
	return nil
}

func (shape secretShape) leafParts() []string {
	var out []string
	for _, p := range shape.parts {
		if p.kind != partForm && len(directParts(p.text).parts) == 0 {
			out = append(out, p.text)
		}
	}
	return out
}

func lastWord(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexAny(strings.TrimRight(s, "="), " \t:$=")+1:]
}

func cookiePair(c string) (value string, ok bool) {
	name, value, ok := strings.Cut(strings.TrimSpace(c), "=")
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	if ok && name != "" && strings.Trim(value, "=") != "" && strings.IndexFunc(name, func(r rune) bool { return !tokenChar(r) }) < 0 &&
		strings.IndexFunc(value, func(r rune) bool { return !cookieChar(r) }) < 0 {
		return value, true
	}
	return "", false
}

func publicPrefixEnd(s string) (int, bool) {
	i := strings.IndexAny(s, "$:")
	return i, i > 0 && i <= maxPrefix && publicPrefix(s[:i])
}

func publicPrefix(p string) bool {
	for i, r := range p {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || !(r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.')) {
			return false
		}
	}
	return p != ""
}

// tokenChar reports whether r may be part of an HTTP token (RFC 9110 section 5.6.2).
func tokenChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

// cookieChar reports whether r may be part of a cookie's value (RFC 6265 section 4.1.1).
func cookieChar(r rune) bool {
	return r > ' ' && r < 0x7f && r != '"' && r != ',' && r != ';' && r != '\\'
}

const minCutFragment = 4

// TrimCut returns head and tail, the start and the end of a body with the middle cut out, without
// the bytes beside the cut that could be the two parts of a secret cut in two, which String
// cannot recognise: a run at the end of head that is the start of a form of the credential, and a
// run at the start of tail that is the end of one, each of at least 4 bytes.
func (r *Redactor) TrimCut(head, tail string) (string, string) {
	if r == nil || r.r == nil {
		return head, tail
	}
	h, t := 0, 0
	for _, f := range r.forms {
		for k := min(len(f)-1, len(head)); k >= max(minCutFragment, h+1); k-- {
			if strings.HasSuffix(head, f[:k]) {
				h = k
				break
			}
		}
		for k := min(len(f)-1, len(tail)); k >= max(minCutFragment, t+1); k-- {
			if strings.HasPrefix(tail, f[len(f)-k:]) {
				t = k
				break
			}
		}
	}
	return head[:len(head)-h], tail[t:]
}

const numberChars = "+-.eE0123456789"

func collectStrings(v any, out []string) []string {
	switch v := v.(type) {
	case string:
		if v != "" {
			out = append(out, v)
		}
	case map[string]any:
		for _, x := range v {
			out = collectStrings(x, out)
		}
	case []any:
		for _, x := range v {
			out = collectStrings(x, out)
		}
	}
	return out
}

const minFragment = 8

func encodings(s string) []string {
	q := strconv.Quote(s)
	out := []string{s, q[1 : len(q)-1], url.QueryEscape(s), url.PathEscape(s)}
	if j, err := json.Marshal(s); err == nil {
		inner := string(j[1 : len(j)-1])
		out = append(out, inner, strings.ReplaceAll(inner, "/", `\/`))
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		out = append(out, enc.EncodeToString([]byte(s)), enc.WithPadding(base64.NoPadding).EncodeToString([]byte(s)))
		for off := range 3 {
			full := enc.WithPadding(base64.NoPadding).EncodeToString(append(make([]byte, off), s...))
			skip := (off*8 + 5) / 6
			end := (off + len(s)) / 3 * 4
			if end-skip >= minFragment {
				out = append(out, full[skip:end])
			}
		}
	}
	return out
}

// String returns s with the credential removed, also where s holds it only once its escapes are
// decoded: s may quote a JSON body or a URL, as error text often does.
func (r *Redactor) String(s string) string {
	if r == nil || r.r == nil {
		return s
	}
	return r.text(s, 0)
}

const maxDepth = 8

func (r *Redactor) text(s string, depth int) string {
	if len(s) < r.min || !r.mayHold(s) {
		return s
	}
	if depth < maxDepth {
		if t := strings.TrimSpace(s); t != "" && strings.IndexByte(`{["`, t[0]) >= 0 && json.Valid([]byte(t)) {
			return string(r.redactJSON([]byte(s), depth+1))
		}
	}
	if r.numeric {
		s = r.jsonNumbers(s)
	}
	out := r.r.Replace(s)
	if r.liveEscape(out) {
		out = r.escaped(out)
	}
	return out
}

func (r *Redactor) jsonNumbers(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != '-' && (c < '0' || c > '9') {
			continue
		}
		end := i
		for end < len(s) && strings.IndexByte(numberChars, s[end]) >= 0 {
			end++
		}
		before := strings.TrimRight(s[:i], " \t\r\n")
		if before != "" && strings.IndexByte(":[,", before[len(before)-1]) >= 0 &&
			(end == len(s) || strings.IndexByte(",]} \t\r\n", s[end]) >= 0) && r.holds(s[i:end]) {
			b.WriteString(s[last:i])
			b.WriteByte('0')
			last = end
		}
		i = end - 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func (r *Redactor) mayHold(s string) bool {
	return r.holds(s) || r.liveEscape(s) || r.fffd && !utf8.ValidString(s)
}

func (r *Redactor) holds(s string) bool {
	var n counter
	_, _ = r.find.WriteString(&n, s)
	return int(n) < len(s)
}

type counter int

func (c *counter) Write(p []byte) (int, error)       { *c += counter(len(p)); return len(p), nil }
func (c *counter) WriteString(s string) (int, error) { *c += counter(len(s)); return len(s), nil }

func (r *Redactor) liveEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' && s[i] != '%' {
			continue
		}
		v, n := escapeAt(s, i, allEscapes)
		if n == 0 {
			continue
		}
		var buf [utf8.UTFMax]byte
		d := buf[:1]
		if buf[0] = byte(v); s[i] == '\\' {
			d = buf[:utf8.EncodeRune(buf[:], v)]
		}
		for _, c := range d {
			if r.chars[c] || strings.IndexByte(`\%0123456789abcdefABCDEF`, c) >= 0 || s[i] == '%' && strings.IndexByte(`ubfnrt"/`, c) >= 0 {
				return true
			}
		}
		i += n - 1
	}
	return false
}

const (
	jsonEscapes = 1 << iota
	percentEscapes
	fixUTF8
	allEscapes = jsonEscapes | percentEscapes
)

func escapeAt(s string, i int, kinds int) (v rune, n int) {
	if s[i] == '%' && kinds&percentEscapes != 0 {
		if i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			return rune(unhex(s[i+1])<<4 | unhex(s[i+2])), 3
		}
		return 0, 0
	}
	if s[i] != '\\' || kinds&jsonEscapes == 0 || i+1 >= len(s) {
		return 0, 0
	}
	switch c := s[i+1]; c {
	case '"', '\\', '/':
		return rune(c), 2
	case 'b':
		return '\b', 2
	case 'f':
		return '\f', 2
	case 'n':
		return '\n', 2
	case 'r':
		return '\r', 2
	case 't':
		return '\t', 2
	case 'u':
		r1, ok := hex4(s, i+2)
		if !ok {
			return 0, 0
		}
		if !utf16.IsSurrogate(r1) {
			return r1, 6
		}
		if i+7 < len(s) && s[i+6] == '\\' && s[i+7] == 'u' {
			if r2, ok := hex4(s, i+8); ok {
				if d := utf16.DecodeRune(r1, r2); d != unicode.ReplacementChar {
					return d, 12
				}
			}
		}
		return unicode.ReplacementChar, 6
	}
	return 0, 0
}

func hex4(s string, i int) (rune, bool) {
	if i+4 > len(s) {
		return 0, false
	}
	var v rune
	for _, c := range []byte(s[i : i+4]) {
		if !isHex(c) {
			return 0, false
		}
		v = v<<4 | rune(unhex(c))
	}
	return v, true
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}

type span struct{ start, end int32 }

func newSpan(start, end int) span { return span{int32(start), int32(end)} }

func (sp span) lo() int { return int(sp.start) }
func (sp span) hi() int { return int(sp.end) }

func endsAfter(spans []span, pos int) int {
	k, _ := slices.BinarySearchFunc(spans, pos, func(sp span, x int) int { return cmp.Compare(sp.hi(), x+1) })
	return k
}

func unescapeLayer(s string, from []span, track bool, kinds int) (string, []span, bool) {
	out := make([]byte, 0, len(s))
	var spans []span
	if track {
		spans = make([]span, 0, len(s))
	}
	changed := false
	for i := 0; i < len(s); {
		v, n := escapeAt(s, i, kinds)
		switch {
		case n == 0 && kinds&fixUTF8 != 0 && s[i] >= utf8.RuneSelf:
			var size int
			if v, size = utf8.DecodeRuneInString(s[i:]); v == utf8.RuneError && size == 1 {
				changed = true
			}
			n = size
			out = utf8.AppendRune(out, v)
		case n == 0:
			n = 1
			out = append(out, s[i])
		default:
			changed = true
			if s[i] == '%' {
				out = append(out, byte(v))
			} else {
				out = utf8.AppendRune(out, v)
			}
		}
		if track {
			sp := newSpan(i, i+n)
			if from != nil {
				sp = span{from[i].start, from[i+n-1].end}
			}
			for len(spans) < len(out) {
				spans = append(spans, sp)
			}
		}
		i += n
	}
	return string(out), spans, changed
}

func (r *Redactor) escaped(s string) string {
	hit, over := r.decodings(s, false, func(d string, _ []span) bool { return r.holds(d) })
	if !hit && !over {
		return s
	}
	var cuts []span
	_, over2 := r.decodings(s, true, func(d string, spans []span) bool {
		for _, f := range r.forms {
			for off := 0; ; {
				k := strings.Index(d[off:], f)
				if k < 0 {
					break
				}
				k += off
				cuts = append(cuts, span{spans[k].start, spans[k+len(f)-1].end})
				off = k + 1
			}
		}
		return false
	})
	if over || over2 {
		return Redacted
	}
	slices.SortFunc(cuts, func(a, b span) int { return cmp.Compare(a.start, b.start) })
	var b strings.Builder
	last := 0
	for i := 0; i < len(cuts); {
		c := cuts[i]
		for i++; i < len(cuts) && cuts[i].start < c.end; i++ {
			c.end = max(c.end, cuts[i].end)
		}
		b.WriteString(s[last:c.start])
		b.WriteString(Redacted)
		last = c.hi()
	}
	b.WriteString(s[last:])
	return b.String()
}

const maxDecodings = 16

func (r *Redactor) decodings(s string, track bool, visit func(string, []span) bool) (found, over bool) {
	type layer struct {
		s    string
		from []span
	}
	seen := map[string]bool{s: true}
	frontier := []layer{{s, nil}}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []layer
		for _, l := range frontier {
			if !r.liveEscape(l.s) {
				continue
			}
			for _, kinds := range []int{allEscapes, jsonEscapes, percentEscapes} {
				d, spans, changed := unescapeLayer(l.s, l.from, track, kinds)
				if !changed || seen[d] {
					continue
				}
				if seen[d] = true; len(seen) > maxDecodings {
					return false, true
				}
				if visit(d, spans) {
					return true, false
				}
				next = append(next, layer{d, spans})
			}
		}
		frontier = next
	}
	return false, false
}

func (r *Redactor) raw(b json.RawMessage) json.RawMessage {
	if r == nil || r.r == nil || len(b) < r.min && !r.fffd || !r.mayHold(view(b)) {
		return b
	}
	var out []byte
	if json.Valid(b) {
		out = r.redactJSON(b, 0)
	} else {
		out = []byte(r.text(string(b), 0))
	}
	if bytes.Equal(out, b) {
		return b
	}
	return llm.RawJSON(out)
}

// Raw returns the JSON b with the credential removed from each of its strings, still valid JSON.
func (r *Redactor) Raw(b json.RawMessage) json.RawMessage { return r.raw(b) }

func view(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }

func (r *Redactor) redactJSON(b []byte, depth int) []byte {
	var out []byte
	last := 0
	for i := 0; i < len(b); i++ {
		if c := b[i]; c == '-' || c >= '0' && c <= '9' {
			end := i
			for end < len(b) && strings.IndexByte(numberChars, b[end]) >= 0 {
				end++
			}
			if tok := b[i:end]; r.numeric && len(tok) >= r.min && r.holds(view(tok)) {
				out = append(append(out, b[last:i]...), '0')
				last = end
			}
			i = end - 1
			continue
		}
		if b[i] != '"' {
			continue
		}
		end := i + 1
		for end < len(b) && b[end] != '"' {
			if b[end] == '\\' {
				end++
			}
			end++
		}
		if content := b[i+1 : end]; (len(content) >= r.min || r.fffd) && r.mayHold(view(content)) {
			s, spans := string(content), []span(nil)
			if bytes.IndexByte(content, '\\') >= 0 || r.fffd && !utf8.Valid(content) {
				s, spans, _ = unescapeLayer(view(content), nil, true, jsonEscapes|fixUTF8)
			}
			if red := r.text(s, depth); red != s {
				out = append(append(out, b[last:i]...), keepBytes(content, s, spans, red)...)
				last = end + 1
			}
		}
		i = end
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

func keepBytes(raw []byte, s string, spans []span, red string) []byte {
	p := 0
	for p < len(s) && p < len(red) && s[p] == red[p] {
		p++
	}
	for p > 0 && (p < len(s) && !utf8.RuneStart(s[p]) || p < len(red) && !utf8.RuneStart(red[p])) {
		p--
	}
	q := 0
	for q < len(s)-p && q < len(red)-p && s[len(s)-1-q] == red[len(red)-1-q] {
		q++
	}
	for q > 0 && (!utf8.RuneStart(s[len(s)-q]) || !utf8.RuneStart(red[len(red)-q])) {
		q--
	}
	rawStart, rawEnd := p, len(raw)-q
	if spans != nil {
		rawStart, rawEnd = 0, len(raw)
		if p > 0 {
			rawStart = spans[p-1].hi()
		}
		if q > 0 {
			rawEnd = spans[len(s)-q].lo()
		}
	}
	mid := encodeString(red[p : len(red)-q])
	out := append([]byte{'"'}, raw[:rawStart]...)
	out = append(out, mid[1:len(mid)-1]...)
	out = append(out, raw[rawEnd:]...)
	return append(out, '"')
}

func encodeString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

var (
	codes = set(llm.CodeInvalidRequest, llm.CodeAuth, llm.CodePermission, llm.CodeAccountClosed, llm.CodeNotFound, llm.CodeRateLimit,
		llm.CodeQuota, llm.CodeUnsupported, llm.CodeUpstream, llm.CodeUnavailable, llm.CodeCancelled, llm.CodeInternal,
		llm.CodeExtensionCrashed, llm.CodeExtensionProtocol, llm.CodeExtensionUnavailable, llm.CodeExtensionTimeout)
	scopes   = set(llm.ScopeRequest, llm.ScopeCredential, llm.ScopeModel, llm.ScopeEndpoint)
	finishes = set(llm.FinishStop, llm.FinishLength, llm.FinishToolCalls, llm.FinishContentFilter, llm.FinishOther)
	formats  = set(llm.FormatOpenAIChat, llm.FormatOpenAIResponses, llm.FormatAnthropicMessages, llm.FormatGeminiGenerate,
		llm.FormatOpenAIResponsesCompact, llm.FormatCodexAlphaSearch, llm.FormatGeminiInteractions,
		llm.FormatOpenAIImagesGenerations, llm.FormatOpenAIImagesEdits, llm.OpaqueDevin)
	mediaKinds     = set(llm.MediaImage, llm.MediaAudio)
	candidateKinds = set(
		llm.CandidateTextDelta, llm.CandidateReasoningDelta,
		llm.CandidateToolCallStart, llm.CandidateToolInputDelta, llm.CandidateToolCallEnd,
		llm.CandidateMedia, llm.CandidateFinish,
	)
)

func set(values ...string) map[string]bool {
	m := map[string]bool{"": true}
	for _, v := range values {
		m[v] = true
	}
	return m
}

func (r *Redactor) field(s string, known map[string]bool) string {
	if known[s] || r.String(s) == s {
		return s
	}
	return Redacted
}

// Event returns ev with the credential removed from the text and upstream data it carries:
// error messages, codes and scopes, every raw field, reasoning's opaque data and its format, the
// translation report, the reply's format and finish reason, and the identifiers the upstream
// names the reply with (its id and model, tool call IDs, the raw finish reason).
// Generated content (deltas, tool names) is the reply itself and is left alone.
func (r *Redactor) Event(ev llm.Event) llm.Event {
	if r == nil || r.r == nil {
		return ev
	}
	ev.Raw = r.raw(ev.Raw)
	if ev.Response != nil {
		resp := *ev.Response
		resp.ID = r.String(resp.ID)
		resp.Model = r.String(resp.Model)
		resp.Raw = r.raw(resp.Raw)
		resp.Format = r.field(resp.Format, formats)
		if t := resp.Translation; t != nil {
			tr := *t
			tr.From, tr.To = r.String(tr.From), r.String(tr.To)
			tr.Losses = slices.Clone(tr.Losses)
			for i, l := range tr.Losses {
				tr.Losses[i] = llm.Loss{Kind: r.String(l.Kind), Path: r.String(l.Path), Detail: r.String(l.Detail)}
			}
			resp.Translation = &tr
		}
		if len(resp.Headers) > 0 {
			h := make(map[string]string, len(resp.Headers))
			for k, v := range resp.Headers {
				h[r.String(k)] = r.String(v)
			}
			resp.Headers = h
		}
		ev.Response = &resp
	}
	if ev.Item != nil {
		it := *ev.Item
		it.CallID = r.String(it.CallID)
		ev.Item = &it
	}
	if ev.Finish != nil {
		f := *ev.Finish
		f.Reason, f.RawReason = r.field(f.Reason, finishes), r.String(f.RawReason)
		ev.Finish = &f
	}
	ev.Usage = r.usage(ev.Usage)
	if ev.Opaque != nil {
		o := *ev.Opaque
		o.Format, o.Data = r.field(o.Format, formats), r.String(o.Data)
		ev.Opaque = &o
	}
	if len(ev.Citations) > 0 {
		cits := slices.Clone(ev.Citations)
		for i := range cits {
			cits[i].URL = r.String(cits[i].URL)
			cits[i].Title = r.String(cits[i].Title)
		}
		ev.Citations = cits
	}
	if len(ev.Sources) > 0 {
		srcs := slices.Clone(ev.Sources)
		for i := range srcs {
			srcs[i].URL = r.String(srcs[i].URL)
			srcs[i].Title = r.String(srcs[i].Title)
		}
		ev.Sources = srcs
	}
	if len(ev.CodeExecutions) > 0 {
		runs := slices.Clone(ev.CodeExecutions)
		for i := range runs {
			runs[i].Code = r.String(runs[i].Code)
			runs[i].Language = r.String(runs[i].Language)
			runs[i].Output = r.String(runs[i].Output)
		}
		ev.CodeExecutions = runs
	}
	if ev.Media != nil {
		m := *ev.Media
		m.Kind = r.field(m.Kind, mediaKinds)
		m.MediaType = r.String(m.MediaType)
		m.Data = r.String(m.Data)
		ev.Media = &m
	}
	if ev.Candidate != nil {
		c := *ev.Candidate
		c.Kind = r.field(c.Kind, candidateKinds)
		c.Delta = r.String(c.Delta)
		c.CallID = r.String(c.CallID)
		c.Name = r.String(c.Name)
		if c.Media != nil {
			m := *c.Media
			m.Kind = r.field(m.Kind, mediaKinds)
			m.MediaType = r.String(m.MediaType)
			m.Data = r.String(m.Data)
			c.Media = &m
		}
		if c.Finish != nil {
			f := *c.Finish
			f.Reason = r.field(f.Reason, finishes)
			f.RawReason = r.String(f.RawReason)
			c.Finish = &f
		}
		ev.Candidate = &c
	}
	if ev.Error != nil {
		e := *ev.Error
		e.Code, e.Scope = r.field(e.Code, codes), r.field(e.Scope, scopes)
		e.Message = r.String(e.Message)
		e.ProviderCode = r.String(e.ProviderCode)
		if e.Dropped != nil {
			e.Dropped = slices.Clone(e.Dropped)
			for i, p := range e.Dropped {
				e.Dropped[i] = r.String(p)
			}
		}
		if len(e.Headers) > 0 {
			h := make(map[string]string, len(e.Headers))
			for k, v := range e.Headers {
				h[r.String(k)] = r.String(v)
			}
			e.Headers = h
		}
		e.Raw = r.raw(e.Raw)
		e.Usage = r.usage(e.Usage)
		ev.Error = &e
	}
	return ev
}

func (r *Redactor) Body() *BodyRedactor {
	b := &BodyRedactor{r: r}
	if r != nil {
		for _, f := range r.forms {
			b.byFirst[f[0]] = append(b.byFirst[f[0]], f)
		}
		for c := range 256 {
			b.spelled[c] = r.chars[c] || strings.IndexByte(escapeSyntax, byte(c)) >= 0 || !utf8.RuneStart(byte(c))
		}
	}
	return b
}

// BodyRedactor removes the credential from a body that arrives in pieces, where a form of it may
// be cut between two pieces or more. Write takes each piece in turn and returns what may be sent
// on now; Flush returns the rest once the body ends (a body that breaks off can drop it instead).
type BodyRedactor struct {
	r       *Redactor
	byFirst [256][]string
	spelled [256]bool
	held    string
	sent    int
	partial string
	cont    bool
	next    []string
	drop    bool
	stretch int
}

const maxNumberHold = 64

const escapeBytes = 12

func (b *BodyRedactor) escapeHold() int { return max(escapeBytes*len(b.r.forms[0]), runHold) }

const runHold = 12 << (maxDepth - 2)

func (b *BodyRedactor) escapeWindow() int { return max(b.escapeHold(), 12*maxDepth) }

func (b *BodyRedactor) maxStretch() int { return 6*maxDepth + len(b.r.forms[0]) }

const escapeSyntax = `\%"/bfnrtu0123456789abcdefABCDEF`

func (b *BodyRedactor) spells(c byte) bool { return b.spelled[c] }

const maxChunk = 16 << 10

// Write returns the redacted text of the body so far that more text cannot change.
func (b *BodyRedactor) Write(piece string) string {
	if b.r == nil || b.r.r == nil {
		return piece
	}
	if b.r.fffd {
		piece = b.fixUTF8(piece, false)
	}
	if len(piece) <= maxChunk {
		return b.write(piece)
	}
	var out strings.Builder
	out.Grow(len(piece))
	for len(piece) > 0 {
		n := min(len(piece), maxChunk)
		for k := n; n < len(piece) && k > n-utf8.UTFMax && k > 0; k-- {
			if utf8.RuneStart(piece[k]) {
				n = k
				break
			}
		}
		out.WriteString(b.write(piece[:n]))
		piece = piece[n:]
	}
	return out.String()
}

func (b *BodyRedactor) write(piece string) string {
	if b.drop {
		return b.dropping(b.held+piece, len(b.held), false)
	}
	if b.cont {
		return b.follow(b.held+piece, false)
	}
	return b.emit(b.held+piece, false)
}

// Flush returns the redacted text the BodyRedactor still holds, for the end of the body. It holds
// nothing after.
func (b *BodyRedactor) Flush() string {
	if b == nil || b.r == nil || b.r.r == nil {
		return ""
	}
	rest := ""
	if b.r.fffd {
		rest = b.fixUTF8("", true)
	}
	if b.drop {
		return b.dropping(b.held+rest, len(b.held), true)
	}
	if b.cont {
		return b.follow(b.held+rest, true)
	}
	return b.emit(b.held+rest, true)
}

func (b *BodyRedactor) fixUTF8(piece string, final bool) string {
	s := b.partial + piece
	b.partial = ""
	if !final {
		for k := 1; k <= min(utf8.UTFMax-1, len(s)); k++ {
			if c := s[len(s)-k]; utf8.RuneStart(c) {
				if c >= utf8.RuneSelf && !utf8.FullRuneInString(s[len(s)-k:]) {
					s, b.partial = s[:len(s)-k], s[len(s)-k:]
				}
				break
			}
		}
	}
	if utf8.ValidString(s) {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		c, n := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && n == 1 {
			out.WriteRune(utf8.RuneError)
		} else {
			out.WriteString(s[i : i+n])
		}
		i += n
	}
	return out.String()
}

func (b *BodyRedactor) dropRun(t string) int {
	for i := 0; i < len(t); i++ {
		c := t[i]
		if utf8.RuneStart(c) && (!b.spells(c) || b.stretch >= b.maxStretch()) {
			return i
		}
		if c == '\\' || c == '%' {
			b.stretch = 0
		} else {
			b.stretch++
		}
	}
	return len(t)
}

func (b *BodyRedactor) emit(s string, final bool) string {
	skip, cut := b.parse(s, b.sent, final)
	if !final {
		cut = b.numberStart(s, cut)
	}
	hold, end, inside := b.escapes(s, b.sent, cut, final)
	if inside < hold && len(s)-inside <= b.escapeHold() {
		hold = inside
	}
	if skip = max(skip, end); hold > b.sent {
		hold = max(hold, skip)
	}
	var lead string
	if skip > b.sent {
		lead = Redacted
	}
	if hold < cut {
		if len(s)-hold > b.escapeHold() {
			return b.replaceSpelling(s, skip, hold, lead)
		}
		if cut = b.numberStart(s, hold); cut > b.sent {
			cut = max(cut, skip)
		}
	}
	if cut <= b.sent {
		b.held, b.sent = strings.Clone(s[cut:]), b.sent-cut
		return ""
	}
	if cut < skip {
		b.held, b.sent = strings.Clone(s[b.sent:]), skip-b.sent
		return lead
	}
	out := b.r.String(s[skip:cut])
	b.held, b.sent = strings.Clone(s[cut:]), 0
	if cut < len(s) {
		if whole := b.r.String(s[skip:]); whole != out+b.r.String(s[cut:]) {
			j := 0
			for j < len(b.held) && !(utf8.RuneStart(b.held[j]) && strings.HasSuffix(whole, b.held[j:])) {
				j++
			}
			if j < len(b.held) && whole[:len(whole)-(len(b.held)-j)] != b.r.String(s[skip:cut+j]) {
				j = len(b.held)
			}
			b.sent = j
			return lead + whole[:len(whole)-(len(b.held)-j)]
		}
	}
	return lead + out
}

func (b *BodyRedactor) replaceSpelling(s string, skip, at int, lead string) string {
	out := lead
	if at > skip {
		out += b.r.String(s[skip:at])
	}
	b.cont, b.next, b.sent = true, nil, len(s)-at
	return out + Redacted + b.follow(s[at:], false)
}

func (b *BodyRedactor) follow(s string, final bool) string {
	dropped := b.sent
	root, ls, over := b.walk(s, false, final)
	if over {
		return b.blind(s, dropped, final)
	}
	ls = append([]layer{root}, ls...)
	y, undecided := dropped, false
	for _, l := range ls {
		for _, rest := range b.next {
			switch {
			case strings.HasPrefix(l.d, rest):
				y = max(y, l.at(len(rest)-1).hi())
			case strings.HasPrefix(rest, l.d):
				undecided = !final
				if final {
					y = len(s)
				}
			}
		}
	}
	for moved := true; moved && !undecided; {
		moved = false
		for _, l := range ls {
			if l.open < y {
				undecided = true
				break
			}
			for _, f := range b.r.forms {
				for off := 0; ; off++ {
					k := strings.Index(l.d[off:], f)
					if k < 0 {
						break
					}
					off += k
					if a, e := l.at(off).lo(), l.at(off+len(f)-1).hi(); a < y && e > y {
						y, moved = e, true
					}
				}
			}
			if k := b.startsForm(l.d); !final && k < len(l.d) && l.at(k).lo() < y {
				undecided = true
				break
			}
			if l.spans != nil {
				if k := endsAfter(l.spans, y); k < len(l.spans) && l.spans[k].lo() < y {
					y, moved = l.spans[k].hi(), true
				}
			}
		}
	}
	if !undecided {
		b.cont, b.next, b.held, b.sent = false, nil, "", 0
		return b.emit(s[y:], final)
	}
	g := len(s)
	for _, l := range ls {
		g = min(g, l.open)
	}
	if g = b.outside(s, 0, ls[1:], g); len(s)-g > b.escapeHold() {
		return b.blind(s, len(s), final)
	}
	next := map[string]bool{}
	for _, l := range ls {
		m := g
		if l.spans != nil {
			m = endsAfter(l.spans, g)
		}
		for _, rest := range b.next {
			if len(rest) > m && strings.HasPrefix(rest, l.d[:m]) {
				next[rest[m:]] = true
			}
		}
		for k := max(0, m-len(b.r.forms[0])+1); k < m; k++ {
			for _, f := range b.byFirst[l.d[k]] {
				if len(f) > m-k && strings.HasPrefix(f, l.d[k:m]) {
					next[f[m-k:]] = true
				}
			}
		}
	}
	b.next = slices.Sorted(maps.Keys(next))
	b.held, b.sent = strings.Clone(s[g:]), len(s)-g
	return ""
}

func (b *BodyRedactor) blind(s string, from int, final bool) string {
	b.cont, b.next = false, nil
	b.stretch = from - (strings.LastIndexAny(s[:from], `\%`) + 1)
	return b.dropping(s, from, final)
}

func (b *BodyRedactor) dropping(s string, from int, final bool) string {
	keep := len(b.r.forms[0]) - 1
	q := from + b.dropRun(s[from:])
	if q == len(s) && !final {
		b.drop, b.held, b.sent = true, strings.Clone(s[max(0, q-keep):]), 0
		return ""
	}
	b.drop, b.held, b.sent = false, "", 0
	k := q
	if q < len(s) && b.stretch >= b.maxStretch() {
		k, b.sent = max(0, q-keep), q-max(0, q-keep)
	}
	return b.emit(s[k:], final)
}

func (b *BodyRedactor) escapes(s string, sent, cut int, final bool) (hold, end, inside int) {
	hold, base, inside := len(s), 0, cut
	if sent == 0 {
		if final || !strings.ContainsAny(s[max(0, len(s)-b.escapeWindow()):], `\%`) {
			return hold, 0, cut
		}
		base = b.spelling(s)
	}
	t := s[base:]
	var ls []layer
	defer func() { inside, hold = b.outside(t, base, ls, inside), b.outside(t, base, ls, hold) }()
	if !b.r.liveEscape(t) && (final || openEscape(t, allEscapes) == len(t) && openEscape(t, jsonEscapes) == len(t) &&
		openEscape(t, percentEscapes) == len(t)) {
		return hold, 0, inside
	}
	root, ls, over := b.walk(t, true, final)
	if over {
		if final {
			return len(s), b.spellsFrom(s, sent), inside
		}
		if end = b.spellsFrom(s, sent); end == len(s) {
			end = 0
		}
		return b.spelling(s), end, inside
	}
	for _, c := range ls {
		if !final {
			hold = min(hold, base+c.open)
			if k := b.startsForm(c.d); k < len(c.d) {
				hold = min(hold, base+c.at(k).lo())
			}
		}
	}
	if sent > 0 {
		from, past := b.past(append([]layer{root}, ls...), sent)
		if past > hold {
			hold = min(hold, from)
		}
		if end = past; end == sent {
			end = 0
		}
	}
	return hold, end, inside
}

type layer struct {
	d     string
	spans []span
	open  int
	live  bool
	tail  string
}

type decodedKey struct{ head, end string }

const keyEnd = 64

func keyOf(d, tail string) decodedKey {
	if len(tail) >= keyEnd {
		d, tail = d+tail, ""
	}
	k := max(0, len(d)+len(tail)-keyEnd)
	if tail == "" {
		return decodedKey{d[:k], d[k:]}
	}
	return decodedKey{d[:k], d[k:] + tail}
}

func (l layer) at(i int) span {
	if l.spans == nil {
		return newSpan(i, i+1)
	}
	return l.spans[i]
}

func (b *BodyRedactor) walk(t string, replace, final bool) (root layer, ls []layer, over bool) {
	root = layer{d: t, open: len(t), live: b.r.liveEscape(t)}
	if replace && b.r.holds(t) {
		root.d, root.spans = b.replaced(t)
	}
	type key struct {
		d, tail string
		open    int
	}
	decoded := map[decodedKey]bool{keyOf(root.d, ""): true}
	seen := map[key]bool{}
	frontier := []layer{root}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []layer
		for _, l := range frontier {
			kinds := []int{allEscapes}
			if strings.IndexByte(l.d, '\\') >= 0 && strings.IndexByte(l.d, '%') >= 0 {
				kinds = append(kinds, jsonEscapes, percentEscapes)
			}
			for _, kind := range kinds {
				p := len(l.d)
				if !final {
					p = openEscape(l.d, kind)
				}
				if !l.live && p == len(l.d) {
					continue
				}
				c := layer{d: l.d[:p], open: l.open}
				if l.spans != nil {
					c.spans = l.spans[:p]
				}
				if p < len(l.d) {
					c.open = l.at(p).lo()
				}
				changed, tailChanged := false, false
				if l.live {
					c.d, c.spans, changed = unescapeLayer(c.d, c.spans, true, kind)
					c.live = b.r.liveEscape(c.d)
				}
				if rest := l.d[p:] + l.tail; rest != "" {
					c.tail, _, tailChanged = unescapeLayer(rest, nil, false, kind)
				}
				if !changed && p == len(l.d) || seen[key{c.d, c.tail, c.open}] {
					continue
				}
				seen[key{c.d, c.tail, c.open}] = true
				if changed || tailChanged {
					if decoded[keyOf(c.d, c.tail)] = true; len(decoded) > maxDecodings {
						return root, ls, true
					}
				}
				next = append(next, c)
				ls = append(ls, c)
			}
		}
		frontier = next
	}
	return root, ls, false
}

func (b *BodyRedactor) spelling(s string) int {
	i := len(s)
	for i > 0 && b.spells(s[i-1]) {
		i--
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

func (b *BodyRedactor) outside(t string, base int, ls []layer, pos int) int {
	for moved := true; moved && pos > base && pos < base+len(t); {
		moved = false
		for k := -1; k < len(ls); k++ {
			d, m, from := t, pos-base, func(i int) int { return i }
			if k >= 0 {
				l := ls[k]
				d, from = l.d, func(i int) int { return l.at(i).lo() }
				if m = endsAfter(l.spans, pos-base); m < len(l.spans) && base+l.spans[m].lo() < pos {
					pos, moved = base+l.spans[m].lo(), true
					break
				}
			}
			if i, _ := escapeHolding(d, m); i < m {
				pos, moved = base+from(i), true
				break
			}
		}
	}
	return pos
}

func escapeHolding(d string, m int) (int, int) {
	for _, kind := range []int{allEscapes, jsonEscapes, percentEscapes} {
		i := 0
		if strings.IndexByte(d[max(0, m-12):m], '\\') < 0 {
			i = max(0, m-2)
			for i < m && d[i] != '%' {
				i++
			}
		}
		for i < m {
			_, n := escapeAt(d, i, kind)
			if i+n > m {
				return i, i + n
			}
			i += max(n, 1)
		}
	}
	return m, m
}

func (b *BodyRedactor) past(ls []layer, pos int) (from, end int) {
	from = pos
	for moved := true; moved; {
		moved = false
		for _, l := range ls {
			m := min(pos, len(l.d))
			if l.spans != nil {
				if m = endsAfter(l.spans, pos); m < len(l.spans) && l.spans[m].lo() < pos {
					from, pos, moved = min(from, l.spans[m].lo()), l.spans[m].hi(), true
					continue
				}
			}
			if i, e := escapeHolding(l.d, m); i < m {
				from, pos, moved = min(from, l.at(i).lo()), l.at(e-1).hi(), true
				continue
			}
			for _, f := range b.r.forms {
				for off := 0; ; {
					k := strings.Index(l.d[off:], f)
					if k < 0 {
						break
					}
					k += off
					if a, e := l.at(k).lo(), l.at(k+len(f)-1).hi(); a < pos && pos < e {
						from, pos, moved = min(from, a), e, true
					}
					off = k + 1
				}
			}
		}
	}
	return from, pos
}

func (b *BodyRedactor) spellsFrom(s string, sent int) int {
	if sent == 0 {
		return 0
	}
	end := sent
	for end < len(s) && b.spells(s[end]) {
		end++
	}
	return end
}

func (b *BodyRedactor) startsForm(d string) int {
	for k := max(0, len(d)-len(b.r.forms[0])+1); k < len(d); k++ {
		for _, f := range b.byFirst[d[k]] {
			if len(f) > len(d)-k && strings.HasPrefix(f, d[k:]) {
				return k
			}
		}
	}
	return len(d)
}

func (b *BodyRedactor) replaced(t string) (string, []span) {
	out := make([]byte, 0, len(t))
	spans := make([]span, 0, len(t))
	for i := 0; i < len(t); {
		n := 0
		for _, f := range b.byFirst[t[i]] {
			if strings.HasPrefix(t[i:], f) {
				n = len(f)
				break
			}
		}
		if n == 0 {
			out, spans = append(out, t[i]), append(spans, newSpan(i, i+1))
			i++
			continue
		}
		out = append(out, Redacted...)
		for range len(Redacted) {
			spans = append(spans, newSpan(i, i+n))
		}
		i += n
	}
	return string(out), spans
}

func openEscape(s string, kinds int) int {
	if kinds&jsonEscapes == 0 || strings.IndexByte(s[max(0, len(s)-12):], '\\') < 0 {
		for i := max(0, len(s)-2); kinds&percentEscapes != 0 && i < len(s); i++ {
			if s[i] == '%' && canGrow(s, i, kinds) {
				return i
			}
		}
		return len(s)
	}
	for i := 0; i < len(s); {
		if canGrow(s, i, kinds) {
			return i
		}
		_, n := escapeAt(s, i, kinds)
		i += max(n, 1)
	}
	return len(s)
}

func canGrow(s string, i int, kinds int) bool {
	rest := s[i+1:]
	switch {
	case s[i] == '%' && kinds&percentEscapes != 0:
		return len(rest) < 2 && allHex(rest)
	case s[i] != '\\' || kinds&jsonEscapes == 0:
		return false
	case rest == "":
		return true
	case rest[0] != 'u':
		return false
	case len(rest) < 5:
		return allHex(rest[1:])
	}
	r1, ok := hex4(s, i+2)
	if !ok || r1 < 0xD800 || r1 > 0xDBFF {
		return false
	}
	next := s[i+6:]
	if len(next) >= 6 {
		return false
	}
	for k := range len(next) {
		if c := next[k]; !(k == 0 && c == '\\' || k == 1 && c == 'u' || k == 2 && (c == 'd' || c == 'D') ||
			k == 3 && strings.IndexByte("cdefCDEF", c) >= 0 || k >= 4 && isHex(c)) {
			return false
		}
	}
	return true
}

func allHex(s string) bool {
	for i := range len(s) {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

func (b *BodyRedactor) parse(s string, sent int, final bool) (skip, cut int) {
	skip = sent
	for i := 0; i < len(s); {
		rest, n := s[i:], 0
		for _, f := range b.byFirst[s[i]] {
			if len(f) > len(rest) {
				if !final && strings.HasPrefix(f, rest) {
					return skip, i
				}
			} else if strings.HasPrefix(rest, f) {
				n = len(f)
				break
			}
		}
		if i < sent && i+n > sent {
			skip = i + n
		}
		i += max(n, 1)
	}
	return skip, len(s)
}

func (b *BodyRedactor) numberStart(s string, cut int) int {
	if !b.r.numeric {
		return cut
	}
	c := cut
	for c > 0 && strings.IndexByte(numberChars, s[c-1]) >= 0 {
		c--
	}
	if c < cut && s[c] != '-' && (s[c] < '0' || s[c] > '9') {
		return cut
	}
	t := strings.TrimRight(s[:c], " \t\r\n")
	if t == "" || strings.IndexByte(":[,", t[len(t)-1]) < 0 || cut-(len(t)-1) > maxNumberHold {
		return cut
	}
	c = len(t) - 1
	last := 0
	for i := 0; i <= c && i < len(s); {
		last = i
		n := 0
		for _, f := range b.byFirst[s[i]] {
			if len(f) <= len(s)-i && strings.HasPrefix(s[i:], f) {
				n = len(f)
				break
			}
		}
		i += max(n, 1)
	}
	return last
}

func (r *Redactor) usage(u *llm.Usage) *llm.Usage {
	if u == nil {
		return nil
	}
	c := *u
	c.Raw = r.raw(c.Raw)
	return &c
}
