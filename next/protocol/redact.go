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

// Redacted replaces a credential in text that leaves a call.
const Redacted = "[redacted]"

// Redactor removes one call's credential from what the call sends back. A nil Redactor changes
// nothing. Core runs every event an
// extension sends through the Redactor of the call's credential before the event reaches a
// client, a log or anything else, so no provider has to remember to.
type Redactor struct {
	r       *strings.Replacer // replaces each form with Redacted; nil when there is nothing to redact
	find    *strings.Replacer // removes each form, to tell whether text holds one without copying it
	forms   []string          // the forms, longest first
	min     int               // length of the shortest form; shorter text cannot hold one
	chars   [256]bool         // the bytes the forms are made of
	numeric bool              // some form could be part of a JSON number
	fffd    bool              // some form holds U+FFFD, which JSON decoders read invalid UTF-8 as
}

// NewRedactor returns the Redactor for the credentials cs. Every string in a credential except its
// kind counts as secret, so a credential kind added later is covered without listing its fields.
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
	// Each line of a secret that spans lines (a PEM private key) is secret on its own too: text
	// read a line at a time, such as a log, never holds the whole secret.
	for _, s := range secrets {
		if strings.Contains(s, "\n") {
			for _, line := range strings.Split(s, "\n") {
				if line = strings.TrimSpace(line); len(line) >= minFragment {
					secrets = append(secrets, line)
				}
			}
		}
	}
	// A secret made of parts is secret part by part too: an upstream may echo only the token
	// behind a public prefix or an authorization scheme, or one cookie of a list.
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
	// strings.Replacer tries its pairs in order at each position, so the longest go first: an
	// encoding that contains another is replaced whole.
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
// bytes long, in the order found; none when s has none of the structures below. NewRedactor
// removes each part of 8 bytes or more (shorter ones would match unrelated text), besides s. With
// a smaller min, a caller tells a secret whose parts are all short, which has some, from one with
// no structure, which has none. These structures are split, and each part split again, up to 4
// times; of what is still left to split then, the last word and the parts nothing splits further
// are parts too, so a token behind more schemes or prefixes than that is still found:
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

// maxPartDepth bounds how many times SecretParts splits a part again.
const maxPartDepth = 4

// maxPrefix is the longest public prefix SecretParts takes off a token.
const maxPrefix = 32

// partKind says what a part of a secret is to the secret around it.
type partKind int

const (
	// partWrapped is what a scheme or public prefix wraps: it is the secret, unless it wraps
	// another part in public words in turn.
	partWrapped partKind = iota
	// partValue is a secret of its own whatever it holds: a cookie's value, Basic's password.
	partValue
	// partForm is another way of writing other parts, Basic's decoded "user:password": removed
	// whole, but its user name is no secret.
	partForm
)

// secretPart is one part of a secret, as directParts reads it.
type secretPart struct {
	text string
	kind partKind
}

// secretShape is how directParts reads a value.
type secretShape struct {
	parts []secretPart
	pair  string // the value's one name=value, when that is all it holds: no scheme or prefix
	mixed bool   // the value is a list of pairs with something in it that is no pair
}

// directParts reads the structures SecretParts lists in s, without splitting its parts again; some
// may be short.
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

// basicParts returns what Basic's base64 text decodes to, when that is text or holds the ":" of a
// user name and password, in any encoding: the decoded value, and the password, or the user name
// when the password is empty.
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

// leafParts returns the parts of a value that nothing splits further, but for a decoded
// "user:password", whose user name is no secret: what a value at the depth limit still holds.
func (shape secretShape) leafParts() []string {
	var out []string
	for _, p := range shape.parts {
		if p.kind != partForm && len(directParts(p.text).parts) == 0 {
			out = append(out, p.text)
		}
	}
	return out
}

// lastWord returns what follows the last space, tab, ":", "$" or "=" of s, base64 padding at its
// end aside: the token of a value nested deeper than SecretParts splits.
func lastWord(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexAny(strings.TrimRight(s, "="), " \t:$=")+1:]
}

// cookiePair reports whether c, one "name=value" of a cookie list (spaces around it aside), is a
// pair that SecretParts reads, and returns its value, its double quotes aside: the name is an HTTP
// token, the value holds only cookie characters, and it is not empty or only "=" characters.
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

// publicPrefixEnd returns the index of the "$" or ":" that ends a public prefix at the start of
// s, if s has one.
func publicPrefixEnd(s string) (int, bool) {
	i := strings.IndexAny(s, "$:")
	return i, i > 0 && i <= maxPrefix && publicPrefix(s[:i])
}

// publicPrefix reports whether p has the shape of a public prefix before a token: a letter, then
// letters, digits, "-", "_" or ".".
func publicPrefix(p string) bool {
	for i, r := range p {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || !(r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.')) {
			return false
		}
	}
	return p != ""
}

// tokenChar reports whether r may be part of an HTTP token (RFC 9110 section 5.6.2), as a
// cookie's name is.
func tokenChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

// cookieChar reports whether r may be part of a cookie's value (RFC 6265 section 4.1.1): printable
// ASCII but space, double quote, comma, semicolon and backslash.
func cookieChar(r rune) bool {
	return r > ' ' && r < 0x7f && r != '"' && r != ',' && r != ';' && r != '\\'
}

// minCutFragment is the shortest part of a secret, at the cut of a body kept in two pieces,
// that TrimCut removes; shorter runs would match unrelated text.
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

// numberChars are the bytes a JSON number is made of.
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

// minFragment is the shortest base64 fragment redacted on its own; shorter ones would match
// unrelated text.
const minFragment = 8

// encodings returns s and the forms it commonly takes in error text and logs: escaped in JSON,
// quoted as Go's %q writes it (a message core builds, such as an event check's), escaped in a
// URL, and base64 (alone, or inside a longer value such as HTTP Basic "user:key").
func encodings(s string) []string {
	q := strconv.Quote(s)
	out := []string{s, q[1 : len(q)-1], url.QueryEscape(s), url.PathEscape(s)}
	if j, err := json.Marshal(s); err == nil {
		inner := string(j[1 : len(j)-1])
		out = append(out, inner, strings.ReplaceAll(inner, "/", `\/`))
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		out = append(out, enc.EncodeToString([]byte(s)), enc.WithPadding(base64.NoPadding).EncodeToString([]byte(s)))
		// Inside a longer value the secret starts at byte offset 0, 1 or 2 of a 3-byte group.
		// The characters of whole groups holding only secret bytes are the same wherever it
		// sits; the characters at either edge also depend on the bytes around it.
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

// maxDepth bounds how many layers of JSON inside strings, and of escapes, are decoded.
const maxDepth = 8

// text returns the decoded string s with the credential removed. A string that is JSON (an
// object, array or string, such as a quoted upstream body) is redacted as JSON in turn, as a
// client parsing it would read it, so it stays JSON. Other text (JSON after other text, malformed
// JSON, percent-encoding) can hold the credential behind escapes that decode to it for a reader;
// where it does, the characters that spell it are replaced and every other byte is kept.
func (r *Redactor) text(s string, depth int) string {
	if len(s) < r.min || !r.mayHold(s) {
		return s
	}
	if depth < maxDepth {
		if t := strings.TrimSpace(s); t != "" && strings.IndexByte(`{["`, t[0]) >= 0 && json.Valid([]byte(t)) {
			// Each string in it is decoded and redacted, its escapes included.
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

// jsonNumbers returns s with each number that holds the credential and stands as a JSON value
// (after a colon, bracket or comma and before the end of a value, as in JSON quoted after other
// text) replaced by 0, so that the JSON stays valid.
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

// mayHold reports whether s may hold the credential: as it is, or behind an escape that could
// decode to part of it (liveEscape). Decoding any other escape cannot reveal the credential.
func (r *Redactor) mayHold(s string) bool {
	return r.holds(s) || r.liveEscape(s) || r.fffd && !utf8.ValidString(s)
}

// holds reports whether s holds a form of the credential as it is. It reads s once and copies
// nothing.
func (r *Redactor) holds(s string) bool {
	var n counter
	_, _ = r.find.WriteString(&n, s)
	return int(n) < len(s)
}

// counter counts the bytes written to it.
type counter int

func (c *counter) Write(p []byte) (int, error)       { *c += counter(len(p)); return len(p), nil }
func (c *counter) WriteString(s string) (int, error) { *c += counter(len(s)); return len(s), nil }

// liveEscape reports whether s holds an escape that could decode to part of the credential: one
// that decodes to a byte the forms are made of, or to a byte that can make another escape with
// the text around it (a backslash, a percent sign or a hex digit; after a percent escape also
// the letter after a backslash). Escapes that decode to anything else, such as \n, \" or
// \u00e9 in ordinary text, leave every form as it was in s.
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

// Kinds of escape a decoding layer decodes, and fixUTF8: read invalid UTF-8 as U+FFFD, as
// encoding/json does.
const (
	jsonEscapes = 1 << iota
	percentEscapes
	fixUTF8
	allEscapes = jsonEscapes | percentEscapes
)

// escapeAt decodes the escape at s[i] if it is of the kinds given: a JSON escape (\n, \u00e9, a
// surrogate pair \ud83d\ude00) or a percent escape (%2F, %2f). It returns what the escape stands
// for (a rune, or for a percent escape a byte) and its length; n is 0 when s[i] starts no such
// escape. As encoding/json does, an unpaired surrogate decodes to U+FFFD.
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

// span is where in the text being redacted some decoded bytes came from: text[start:end]. Its
// fields are 32-bit, which halves what decoding a text takes; a text redacted is far shorter
// than 2 GiB (a frame is at most 64 MiB).
type span struct{ start, end int32 }

// newSpan returns the span text[start:end].
func newSpan(start, end int) span { return span{int32(start), int32(end)} }

func (sp span) lo() int { return int(sp.start) }
func (sp span) hi() int { return int(sp.end) }

// endsAfter returns the index of the first of spans, which are in order, that ends after pos.
func endsAfter(spans []span, pos int) int {
	k, _ := slices.BinarySearchFunc(spans, pos, func(sp span, x int) int { return cmp.Compare(sp.hi(), x+1) })
	return k
}

// unescapeLayer decodes one layer of the escapes of the given kinds in s (escapeAt) and reports
// whether there were any. With track it also returns, for each byte of the result, the span of
// the original text it came from, given from: the same for each byte of s (nil when s is the
// original text).
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

// escaped returns s with the credential replaced wherever s holds it once its escapes are
// decoded, up to maxDepth layers: only the characters of s that spell it are replaced, so every
// other escape keeps its bytes.
func (r *Redactor) escaped(s string) string {
	// Most text with escapes does not hold the credential behind them: find out first, without
	// tracking where each byte came from.
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

// maxDecodings bounds how many different decodings of one text are tried. Text with more is
// redacted whole, rather than risk missing one.
const maxDecodings = 16

// decodings calls visit with each different decoding of s, layer after layer up to maxDepth, until
// visit returns true. A reader may decode JSON and percent escapes in either order, so each layer
// is decoded each way and both at once; a layer without a live escape reveals nothing more. It
// reports whether visit returned true, and whether s had more than maxDecodings decodings.
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

// raw returns b with the credential removed, still valid JSON. A client decodes the JSON, so
// the credential is removed from each decoded string rather than from b's bytes: an upstream
// can escape any character of it (\u0073 for "s"), and no list of byte encodings covers them
// all. Data that cannot hold it is returned as it is, without being copied or decoded.
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

// view returns b as a string without copying it, for reading while b does not change.
func view(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }

// redactJSON returns the valid JSON b with the credential removed from every string in it, object
// keys included, and from every number, which becomes 0 so that it stays a number. Only tokens
// that change are written again; every other byte is kept.
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
		// A decoded string is no longer than its token (unless invalid UTF-8 becomes U+FFFD), so
		// a short token cannot hold the credential.
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

// keepBytes returns the JSON string token for red, the redaction of s, which is the decoding of
// the token content raw (spans maps each byte of s to raw; nil when s is raw). The bytes before
// the first change and after the last keep their escapes as raw has them.
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

// Values core acts on exactly, as routing does on an error's code and scope. A credential that
// is part of one of these does not change it; any other value that holds a credential is
// replaced whole (field), so a very short credential cannot turn a value core knows into one it
// does not. A value added to llm later and missing here is still passed on unless it holds a
// credential.
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

// field returns s as it is when known holds it or it holds no credential, and Redacted otherwise.
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

// Body returns a BodyRedactor for a body r's call sends in pieces, such as an endpoint reply's.
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
//
// It follows the listed forms (encodings) as String's replacer does: at each position the longest
// form that starts there is replaced, and the search goes on after it. Write returns the text up
// to the first position where more text could change that, and holds back the rest: fewer bytes
// than the longest form. When a form could be part of a JSON number, it also holds back a colon,
// bracket or comma at the end, with any spaces after it and the number that follows, if any (a run
// of the bytes of numbers that starts with a digit or a minus), when they take at most
// maxNumberHold bytes, and where they start inside a form, that form. Each text it returns is
// redacted with String, so a credential spelled with escapes is found within it, and a number in
// JSON that holds one becomes 0.
//
// It follows the credential behind escapes too, through each decoding String tries (escaped), so
// that a credential spelled with escapes outside the listed forms (\u0073 for s, %73, \\u0073 in
// JSON quoted twice) is removed wherever a cut falls. In each decoding it holds back from an escape
// at the end that more text could complete or change (a backslash, \u with fewer than four hex
// digits, the first half of a surrogate pair, a percent sign with fewer than two), and from the
// start of a run at the end of the decoded text that is the start of a form; and it never cuts
// inside an escape that a decoding reads, which the text after the cut would read otherwise. It
// holds back at most escapeHold bytes for a spelling: enough for a run shorter than the longest
// form written with escapes of up to 11 bytes a byte (\u0073 is 6, \\u0073 7, %5Cu0073 8,
// \u005cu0073 11), an escape at the end, and the start of a listed form after them, and at least
// runHold, enough for an escape at the end of a run of escapes nested by escaping the backslash
// again (\\\\, \u005c\u005c), so that such a run comes through as it is. A spelling that needs
// more (escapes of escapes, as each character of \u0073 escaped again at 36 bytes a byte) is
// replaced with [redacted] from where it starts, and so is what goes on with it in a decoding, up
// to the end of the form it began (follow); what follows that cannot go on with it comes through.
// Where following it would take more than escapeHold bytes (an escape nested so deep that what
// more text could complete of it takes more), or where what goes on with it has more decodings
// than String tries, so that the forms in it are not known, Write drops instead what follows that
// could still be part of a spelling (dropRun): bytes that can be part of one (spells), no more
// than maxStretch of them in a row without a backslash or a percent sign. Where it stops at that
// bound, it keeps the last bytes it dropped, fewer than the longest form, counted as sent, so a
// form that began in them is replaced whole (dropping). Text with more decodings than String tries
// (maxDecodings), which String replaces whole, is held back from the start of the run of such
// bytes at its end, within the same bound.
//
// When a form holds U+FFFD, Write first reads each byte of invalid UTF-8 as U+FFFD, as a JSON
// decoder does and as String does in text that is JSON, holding back the start of a character at
// the end of a piece until the next piece completes it.
//
// So a body of any length passes through in bounded memory: Write reads a piece in parts of at most
// maxChunk bytes, and after each Write it holds back at most escapeHold bytes, or, when a form could
// be part of a number, escapeHold, maxNumberHold and the longest form together, and the start of a
// character; for one Write more where the rest of a form that began in what was sent goes on past
// where a listed form starts (emit). After Flush it holds nothing. A stream is delayed only where a
// piece ends with what could start a form or complete an escape, or with such a number.
//
// It is never worse than redacting each piece on its own: where holding back would change what
// String finds in the text whole (for example text String gives up on and replaces whole), Write
// returns all of the text it has, redacted whole, but for the end of what it would have held that
// this left as it was, which it goes on holding back. It keeps the rest of that end too, marked as
// sent: a form that starts there and that later pieces complete is replaced in them, so the rest of
// a credential cut there is not sent; what was sent of it before the cut stays sent. So is an
// escape of any decoding that starts there and that later pieces complete, up to where no escape
// or form of a decoding spans (past): what the client got for that end may be [redacted], after
// which it reads what follows on its own, so the rest of such an escape could start a spelling for
// it. Where the text then has more decodings than String tries, so that the forms in it are not
// known, the run of bytes that can be part of a spelling that goes on from what was sent is
// replaced instead, or, while it goes on to the end of what came, held back.
//
// shortcut: a number that, with what stands before it, takes more than maxNumberHold bytes and is
// cut between pieces may be redacted as text ([redacted]) or as two numbers (0 and the rest of its
// digits) where String would write 0, either of which leaves JSON invalid. String reads text that
// is JSON as JSON, and Write calls it on each text it returns, so where a cut makes a text JSON
// that the whole body is not (a JSON string on its own), or the other way round, a number holding a
// credential may become 0 where String on the whole body writes [redacted], or the other way round;
// the credential is removed either way. Upgrade the first if an upstream is seen to write
// credentials that way in a streamed body.
type BodyRedactor struct {
	r       *Redactor
	byFirst [256][]string // the forms by their first byte, longest first
	spelled [256]bool     // the bytes that can be part of a spelling of a form (spells)
	// held is the end of the body so far, which more text could still change; its first sent
	// bytes were already returned, redacted whole, by a Write that could not hold them back.
	held string
	sent int
	// partial is the start of a UTF-8 character that a piece ended with, when a form holds U+FFFD.
	partial string
	// cont is set after a spelling of a form was replaced with [redacted] because holding it back
	// would take more than escapeHold bytes: held is then the end of what was replaced, from a
	// position that no escape in a decoding spans, and Write drops what could still go on with a
	// form that began before (follow).
	cont bool
	// next are the rests of the forms that what was replaced before held ends with the start of,
	// one of which held may go on with.
	next []string
	// drop is set instead where that end would take more than escapeHold bytes: Write drops what
	// comes while it could still be part of a spelling (dropRun), and held is the end of what it
	// dropped, fewer bytes than the longest form (dropping). stretch counts the bytes since the
	// last backslash or percent sign.
	drop    bool
	stretch int
}

// maxNumberHold bounds how much of a JSON number, and of what stands before it, Write holds back
// besides a form's start, so that String reads the number whole.
const maxNumberHold = 64

// escapeBytes is how many bytes of a body Write holds back at most, for a spelling of a form with
// escapes that more text could complete, per byte of the longest form.
const escapeBytes = 12

// escapeHold returns how many bytes Write holds back at most for a spelling of a form with escapes
// that more text could complete: escapeBytes for each byte of the longest form, L, and at least
// runHold. That covers a run of at most L-1 decoded bytes written in at most 11 bytes each, an
// escape at the end that more text could complete (at most 11 bytes, as \ud83d\udc0), and the
// start of a listed form after them (at most L-1 bytes): 11(L-1) + 11 + L-1 < 12L.
func (b *BodyRedactor) escapeHold() int { return max(escapeBytes*len(b.r.forms[0]), runHold) }

// runHold is the least escapeHold, enough for an escape at the end that more text could complete
// in a run of escapes nested by escaping the backslash again at each layer (\\ in JSON quoted
// again and again), where each byte of the first decoding comes from an escape of at most 6
// bytes (\u005c): a backslash of the last layer that is decoded further takes 6·2^(maxDepth-2)
// bytes, and what follows it that the layers under it hold open less than that again. So text made
// only of such escapes, such as a run of backslashes, is held back from no further back than that,
// and comes through as it is.
const runHold = 12 << (maxDepth - 2)

// escapeWindow returns how far from the end of the text an escape of a spelling that more text
// could complete lies at most, unless the spelling takes more than escapeHold bytes: such a
// spelling ends with fewer bytes that stand for themselves than the longest form, or with escapes
// that more text could complete, at most 12 bytes for each layer of decoding.
func (b *BodyRedactor) escapeWindow() int { return max(b.escapeHold(), 12*maxDepth) }

// maxStretch returns how many bytes in a row without a backslash or a percent sign a spelling of a
// form can hold. Of such a run, each layer of decoding can take only what the one escape that
// starts just before it takes (at most the 5 bytes of uXXXX), and what is left after maxDepth
// layers stands for itself, so it is part of the form: at most 6 bytes a layer and the longest
// form.
func (b *BodyRedactor) maxStretch() int { return 6*maxDepth + len(b.r.forms[0]) }

// escapeSyntax are the bytes, besides a form's own, that an escape that decodes to part of a form
// is made of (escapeAt).
const escapeSyntax = `\%"/bfnrtu0123456789abcdefABCDEF`

// spells reports whether c can be part of a spelling of a form, as it is or behind escapes nested
// any number of times: in each decoding any other byte stands for itself and is in no form, so no
// form a decoding holds spans it. A byte that continues a UTF-8 character counts as one that can,
// so that what follows one that cannot starts a character.
func (b *BodyRedactor) spells(c byte) bool { return b.spelled[c] }

// maxChunk is the most of a piece Write reads at once: a larger piece is read in parts, so that
// what decoding it takes stays bounded however large the piece. Each part ends where a character
// starts, as every other cut does: text replaced whole with [redacted] at a part's end takes the
// whole character with it, not the start of one whose rest the next part would send.
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

// write is Write for one part of a piece.
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

// fixUTF8 returns piece, after the start of a character the last piece ended with, with each byte
// of invalid UTF-8 read as U+FFFD. Unless final, it keeps the start of a character at the end for
// the next piece.
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

// dropRun returns how much of t to drop while dropping what may continue a spelling that was
// replaced, where follow cannot tell: bytes that can be part of a spelling (spells), as long as the
// run without a backslash or a percent sign is no longer than maxStretch, and the bytes that end
// the character a dropped byte began.
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

// emit returns what can be sent of s, the held text and what came after it, and keeps the rest;
// with final nothing more comes, and it keeps nothing. s[:b.sent] was already sent (or dropped:
// dropping).
func (b *BodyRedactor) emit(s string, final bool) string {
	skip, cut := b.parse(s, b.sent, final)
	if !final {
		cut = b.numberStart(s, cut)
	}
	hold, end, inside := b.escapes(s, b.sent, cut, final)
	if inside < hold && len(s)-inside <= b.escapeHold() {
		// Not inside an escape either, which the text after the cut would read differently.
		hold = inside
	}
	if skip = max(skip, end); hold > b.sent {
		// Not inside the rest of a form that began in what was sent: that is replaced whole.
		hold = max(hold, skip)
	}
	var lead string
	if skip > b.sent {
		// A form that began in what was sent ends here: its rest is not sent.
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
		// Nothing new is decided: hold it all from cut, also what was sent of it, so that a form
		// that starts there is still found once more text comes.
		b.held, b.sent = strings.Clone(s[cut:]), b.sent-cut
		return ""
	}
	if cut < skip {
		// The rest of a form that began in what was sent goes on past where a listed form or a
		// number that more text could change starts: hold back all from sent, that rest counted
		// as sent, so that the form that starts inside it is still found.
		b.held, b.sent = strings.Clone(s[b.sent:]), skip-b.sent
		return lead
	}
	out := b.r.String(s[skip:cut])
	b.held, b.sent = strings.Clone(s[cut:]), 0 // not keeping all of a large piece alive
	if cut < len(s) {
		if whole := b.r.String(s[skip:]); whole != out+b.r.String(s[cut:]) {
			// The cut would hide what String finds in the text whole: send it whole, and keep the
			// end, so that a form starting there is still removed from what follows. Of that end,
			// what the text whole changed counts as sent; the rest, the longest end of it that the
			// text whole ends with as it was, is held back as before rather than sent, when
			// redacting the text up to there gives the rest of the text whole (that the text whole
			// merely ends like it, as a 0 standing for a number that ends in 0 does, is not
			// enough). Otherwise all of it counts as sent.
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

// replaceSpelling returns what to send of s when what Write would hold back from at, the start of
// a spelling of a form with escapes, takes more than escapeHold bytes: the text before at,
// redacted, and [redacted] in place of the spelling and of what may go on with it (follow). No
// escape in a decoding spans at, so the text from there decodes as it does in s.
func (b *BodyRedactor) replaceSpelling(s string, skip, at int, lead string) string {
	out := lead
	if at > skip {
		out += b.r.String(s[skip:at])
	}
	b.cont, b.next, b.sent = true, nil, len(s)-at
	return out + Redacted + b.follow(s[at:], false)
}

// follow returns what to send of s, the text that a spelling replaced with [redacted] ended with,
// from a position that no escape in a decoding spans, and what came after it; s[:b.sent] was
// replaced. It drops the text up to the first position y from which sending s goes on with no
// form that began before y in a decoding, and goes on from there as Write does (emit): y is past
// the end of every form a decoding holds that starts before it, and no escape in a decoding
// spans it. A form that began before s shows in its decodings as a run at their start that is one
// of next, which is dropped too. While a form that began before y could still go on, or an escape
// that began before y could still change, it keeps the end of s from the last position no escape
// spans, with the rests of the forms that what it drops ends with the start of, unless that takes
// more than escapeHold bytes (blind).
func (b *BodyRedactor) follow(s string, final bool) string {
	dropped := b.sent
	root, ls, over := b.walk(s, false, final)
	if over {
		// The forms in s are not known.
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
				// What is decided of l so far may still be the rest of a form.
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
				undecided = true // an escape that began before y could still change
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
				// No escape a decoding reads spans y.
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
	// What each decoding holds before g that a form could go on from: the rest of one of next
	// that it starts with, or a run at its end that is the start of a form.
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

// blind returns what to send of s, which follow cannot follow, of which s[:from] was replaced: it
// drops what may still be part of the spelling replaced (dropRun), and goes on after it as Write
// does.
func (b *BodyRedactor) blind(s string, from int, final bool) string {
	b.cont, b.next = false, nil
	b.stretch = from - (strings.LastIndexAny(s[:from], `\%`) + 1)
	return b.dropping(s, from, final)
}

// dropping returns what to send of s, of which s[:from] was dropped: it drops what may still be
// part of the spelling replaced (dropRun), and goes on after it as Write does. Where it stops at
// maxStretch bytes in a row without a backslash or a percent sign, a form may have begun in what
// it dropped: no escape of any decoding reaches into the last L bytes of such a run (an escape and
// what each layer under it makes of what follows take at most 6 bytes a layer, 6·maxDepth in all),
// so in every decoding they are themselves, and a form that goes on past the stop began in its
// last L-1 bytes, as they are. So it keeps those, counted as sent, and emit replaces such a form
// as one that began in what was sent. While it drops to the end of s, it keeps them in held.
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

// escapes follows the forms through each decoding String tries of s (decodings), as String finds
// them behind escapes once the listed forms are replaced, and returns hold, end and inside. hold
// is len(s), or the earlier start of a spelling of a form that more text could complete or change:
// an escape at the end of a decoding that more text could complete, or a run at the end of one
// that is the start of a form. end is 0, or, when an escape or a form of a decoding starts before
// sent and ends after it, the first position past sent that none spans (past); while what more
// text could change of a decoding starts before that position, hold is at most where the first
// escape or form that spans sent starts. inside is cut, or the start of an escape a decoding reads
// that holds cut: the text from cut, read on its own, would read it differently. With final nothing
// more comes, so hold is len(s). Where s has more decodings than maxDecodings, hold is the start
// of the run of bytes that can be part of a spelling (spells) at the end of s, and end, when
// something was sent, the end of the run of them that goes on from sent: the forms in s are not
// known, and one could have begun in what was sent.
//
// It reads s as String reads it whole, also what parse holds back as the start of a listed form: a
// form a decoding holds that a cut would split (a listed form that starts inside an escape) is
// left to emit, which then sends the text whole. When nothing of s was sent, only its end can
// hold a spelling that more text could complete, so it reads only the run at the end of s that
// could be part of one (spelling): no spelling spans a byte before it, and a decoding of the
// run is the end of the decoding of s.
func (b *BodyRedactor) escapes(s string, sent, cut int, final bool) (hold, end, inside int) {
	hold, base, inside := len(s), 0, cut
	if sent == 0 {
		if final || strings.IndexAny(s[max(0, len(s)-b.escapeWindow()):], `\%`) < 0 {
			// A spelling that more text could complete has an escape in its last escapeWindow
			// bytes, or Write would replace it from its start anyway; and an escape that holds
			// cut is in them too.
			return hold, 0, cut
		}
		base = b.spelling(s)
	}
	t := s[base:]
	var ls []layer // the decodings of t, for outside
	defer func() { inside, hold = b.outside(t, base, ls, inside), b.outside(t, base, ls, hold) }()
	if !b.r.liveEscape(t) && (final || openEscape(t, allEscapes) == len(t) && openEscape(t, jsonEscapes) == len(t) &&
		openEscape(t, percentEscapes) == len(t)) {
		// No escape could decode to part of a form, nor one at the end become one.
		return hold, 0, inside
	}
	root, ls, over := b.walk(t, true, final)
	if over {
		// String gives up on text with this many decodings.
		if final {
			return len(s), b.spellsFrom(s, sent), inside
		}
		// A run that goes on to the end of s is held back, as sent as it was.
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
		// base is 0. What the client got of s[:sent] is not s[:sent] where it was redacted whole,
		// so the client reads the text after an escape or form that sent cuts on its own: such
		// an escape or form is replaced too, from sent to past where they end.
		from, past := b.past(append([]layer{root}, ls...), sent)
		if past > hold {
			// What more text could change of a decoding starts before that: decide nothing yet,
			// and hold back from where what spans sent starts, so it is still known next time.
			hold = min(hold, from)
		}
		if end = past; end == sent {
			end = 0
		}
	}
	return hold, end, inside
}

// layer is a decoding of a text t, or what of one more text cannot change: d, whose bytes came from
// t[spans[i].start:spans[i].end] (from t[i] when spans is nil); open, where in t the text that
// could still change it starts; and tail, what that text decodes to as it is, so that d and tail
// are the decoding of t as it is.
type layer struct {
	d     string
	spans []span
	open  int
	live  bool // d holds an escape that could decode to part of a form (liveEscape)
	tail  string
}

// decodedKey stands for a text in a map without copying all of it: its last keyEnd bytes (or all of
// it when shorter) and the rest. Two keys are equal when the texts are.
type decodedKey struct{ head, end string }

// keyEnd is how many bytes at the end of a text a decodedKey keeps apart.
const keyEnd = 64

// keyOf returns the decodedKey of d+tail, copying only what of the end of d goes with tail when
// tail is shorter than keyEnd.
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

// at returns where in t the byte d[i] came from.
func (l layer) at(i int) span {
	if l.spans == nil {
		return newSpan(i, i+1)
	}
	return l.spans[i]
}

// walk returns root, t (with each listed form replaced as String's replacer replaces it when
// replace), and the decodings of it that String tries (decodings), each as far as more text cannot
// change it unless final, and whether there are more of them than maxDecodings, String counting
// each by what it decodes t to as it is (with its tail); then the decodings are not all there.
func (b *BodyRedactor) walk(t string, replace, final bool) (root layer, ls []layer, over bool) {
	root = layer{d: t, open: len(t), live: b.r.liveEscape(t)}
	if replace && b.r.holds(t) {
		root.d, root.spans = b.replaced(t)
	}
	type key struct {
		d, tail string
		open    int
	}
	// decoded counts the decodings, as decodings does; seen keeps each layer once.
	decoded := map[decodedKey]bool{keyOf(root.d, ""): true}
	seen := map[key]bool{}
	frontier := []layer{root}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []layer
		for _, l := range frontier {
			kinds := []int{allEscapes}
			if strings.IndexByte(l.d, '\\') >= 0 && strings.IndexByte(l.d, '%') >= 0 {
				// Otherwise decoding only one kind gives what decoding both gives, or nothing.
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
					// A layer that is not live gives one that is not either: its text is a start of it.
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

// spelling returns where the run of bytes that can be part of a spelling of a form (spells) at the
// end of s starts, past the bytes that continue the character before it, which cannot be.
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

// outside returns pos, or, when it falls inside an escape of t or of a decoding of t (ls), the start
// of that escape: the text from pos on, read on its own, would read it otherwise. That holds for an
// escape in a decoding that is not decoded further too, which more text can make one that is. t
// starts at base in the text.
func (b *BodyRedactor) outside(t string, base int, ls []layer, pos int) int {
	for moved := true; moved && pos > base && pos < base+len(t); {
		moved = false
		for k := -1; k < len(ls); k++ {
			d, m, from := t, pos-base, func(i int) int { return i }
			if k >= 0 {
				l := ls[k]
				d, from = l.d, func(i int) int { return l.at(i).lo() }
				// The first byte whose spelling ends past pos holds pos when it starts before it.
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

// escapeHolding returns where an escape of any kind in d that holds d[m] starts and ends, or m and
// m when none does.
func escapeHolding(d string, m int) (int, int) {
	for _, kind := range []int{allEscapes, jsonEscapes, percentEscapes} {
		// An escape that holds m starts at most 12 bytes before it. Without a backslash there,
		// only a percent sign in the 2 bytes before can start one, and no escape holds a percent
		// sign, so each is where unescapeLayer stops.
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

// past returns pos, or the first position after it that no escape of a layer of ls spans, also one
// a layer holds that is not decoded further, nor a form a layer holds: the text from there, read
// on its own, decodes as it does after it in each layer. from is pos, or where the first escape or
// form that spans pos starts.
func (b *BodyRedactor) past(ls []layer, pos int) (from, end int) {
	from = pos
	for moved := true; moved; {
		moved = false
		for _, l := range ls {
			m := min(pos, len(l.d)) // a layer that stops at an open escape before pos
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

// spellsFrom returns where the run of bytes that can be part of a spelling (spells) that goes on
// from sent ends; 0 when sent is 0.
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

// startsForm returns where the first run at the end of d starts that is the start of a form longer
// than it, which more text could complete; len(d) when there is none.
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

// replaced returns t with each listed form replaced as String's replacer replaces it, and where in
// t each of its bytes came from.
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

// openEscape returns the first position in s, going through it as unescapeLayer does with the
// given kinds, of an escape that more text after s could complete or change; len(s) when there is
// none.
func openEscape(s string, kinds int) int {
	if kinds&jsonEscapes == 0 || strings.IndexByte(s[max(0, len(s)-12):], '\\') < 0 {
		// Only a percent sign in the last two bytes can be one, and no escape holds a percent
		// sign, so each is where unescapeLayer stops.
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

// canGrow reports whether more text after s could make s[i:] start an escape of the given kinds
// other than the one it starts now: a percent sign followed by fewer than two hex digits, a
// backslash at the end, \u followed by fewer than four, or the first half of a surrogate pair
// followed by what could be the start of the second half.
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
	// A second half is \uDC00 to \uDFFF.
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

// parse follows the forms through s and returns cut, how much of s more text after it cannot
// change once redacted (all of s when final), and skip: sent, or the end of a form that starts
// before sent and ends after it. A position at which a form longer than the rest of s starts with
// that rest is undecided, since more text could complete it; so is everything after. Forms are
// longest first, so the first that matches is the longest.
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

// numberStart returns where to cut s instead of at cut, a position parse stopped at, so that a
// JSON number is not cut from its digits or from the colon, bracket or comma before it, which
// String needs to read it as a number (jsonNumbers). When the Redactor has a form that could be
// part of a number and s[:cut] ends with such a character, any spaces, and either nothing more or
// a run of the bytes of numbers that starts with a minus or a digit, it returns the position of
// that character (or of the form it falls in) if that is within maxNumberHold of cut; cut
// otherwise. Other runs of those bytes, such as the "e" ending "there", are never a number.
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
	// c must be a position parse stops at, not inside a form.
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
