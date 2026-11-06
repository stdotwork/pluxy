package protocol

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
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
	}
	red.r, red.find = strings.NewReplacer(pairs...), strings.NewReplacer(gone...)
	return red
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
// escaped in a URL, and base64 (alone, or inside a longer value such as HTTP Basic "user:key").
func encodings(s string) []string {
	out := []string{s, url.QueryEscape(s), url.PathEscape(s)}
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
	out := r.r.Replace(s)
	if r.liveEscape(out) {
		out = r.escaped(out)
	}
	return out
}

// mayHold reports whether s may hold the credential: as it is, or behind an escape that could
// decode to part of it (liveEscape). Decoding any other escape cannot reveal the credential.
func (r *Redactor) mayHold(s string) bool {
	return r.holds(s) || r.liveEscape(s)
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
		v, n := escapeAt(s, i)
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

// escapeAt decodes the escape at s[i], a JSON escape (\n, \u00e9, a surrogate pair \ud83d\ude00)
// or a percent escape (%2F, %2f), and returns what it stands for (a rune, or for a percent
// escape a byte) and its length. n is 0 when s[i] starts no escape. As encoding/json does, an
// unpaired surrogate decodes to U+FFFD.
func escapeAt(s string, i int) (v rune, n int) {
	if s[i] == '%' {
		if i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			return rune(unhex(s[i+1])<<4 | unhex(s[i+2])), 3
		}
		return 0, 0
	}
	if s[i] != '\\' || i+1 >= len(s) {
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

// span is where in the text being redacted some decoded bytes came from: text[start:end].
type span struct{ start, end int }

// unescapeLayer decodes one layer of the escapes in s (escapeAt) and reports whether there were
// any. With track it also returns, for each byte of the result, the span of the original text
// it came from, given from: the same for each byte of s (nil when s is the original text).
func unescapeLayer(s string, from []span, track bool) (string, []span, bool) {
	out := make([]byte, 0, len(s))
	var spans []span
	if track {
		spans = make([]span, 0, len(s))
	}
	changed := false
	for i := 0; i < len(s); {
		v, n := escapeAt(s, i)
		if n == 0 {
			n = 1
			out = append(out, s[i])
		} else {
			changed = true
			if s[i] == '%' {
				out = append(out, byte(v))
			} else {
				out = utf8.AppendRune(out, v)
			}
		}
		if track {
			sp := span{i, i + n}
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
// decoded, layer after layer up to maxDepth: only the characters of s that spell it are
// replaced, so every other escape keeps its bytes.
func (r *Redactor) escaped(s string) string {
	// Most text with escapes does not hold the credential behind them: find out first, without
	// tracking where each byte came from. A layer without a live escape reveals nothing more.
	hit := false
	for cur, i := s, 0; !hit && i < maxDepth && r.liveEscape(cur); i++ {
		next, _, _ := unescapeLayer(cur, nil, false)
		hit, cur = r.holds(next), next
	}
	if !hit {
		return s
	}
	var cuts []span
	cur, from := s, []span(nil)
	for range maxDepth {
		next, spans, changed := unescapeLayer(cur, from, true)
		if !changed {
			break
		}
		for _, f := range r.forms {
			for off := 0; ; {
				k := strings.Index(next[off:], f)
				if k < 0 {
					break
				}
				k += off
				cuts = append(cuts, span{spans[k].start, spans[k+len(f)-1].end})
				off = k + 1
			}
		}
		cur, from = next, spans
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
		last = c.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// raw returns b with the credential removed, still valid JSON. A client decodes the JSON, so
// the credential is removed from each decoded string rather than from b's bytes: an upstream
// can escape any character of it (\u0073 for "s"), and no list of byte encodings covers them
// all. Data that cannot hold it is returned as it is, without being copied or decoded.
func (r *Redactor) raw(b json.RawMessage) json.RawMessage {
	if r == nil || r.r == nil || len(b) < r.min || !r.mayHold(view(b)) {
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
		// A decoded string is no longer than its token, so a short token cannot hold the
		// credential.
		if content := b[i+1 : end]; len(content) >= r.min && r.mayHold(view(content)) {
			var s string
			if bytes.IndexByte(content, '\\') < 0 {
				s = string(content)
			} else if json.Unmarshal(b[i:end+1], &s) != nil {
				s = ""
			}
			if red := r.text(s, depth); red != s {
				out = append(append(out, b[last:i]...), encodeString(red)...)
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

func encodeString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// Event returns ev with the credential removed from the text and upstream data it carries:
// error messages and codes, every raw field and reasoning's opaque data. Generated content (deltas, tool names) is the
// reply itself and is left alone.
func (r *Redactor) Event(ev llm.Event) llm.Event {
	if r == nil || r.r == nil {
		return ev
	}
	ev.Raw = r.raw(ev.Raw)
	if ev.Response != nil {
		resp := *ev.Response
		resp.Raw = r.raw(resp.Raw)
		ev.Response = &resp
	}
	ev.Usage = r.usage(ev.Usage)
	if ev.Opaque != nil {
		o := *ev.Opaque
		o.Data = r.String(o.Data)
		ev.Opaque = &o
	}
	if ev.Error != nil {
		e := *ev.Error
		e.Message = r.String(e.Message)
		e.ProviderCode = r.String(e.ProviderCode)
		e.Raw = r.raw(e.Raw)
		e.Usage = r.usage(e.Usage)
		ev.Error = &e
	}
	return ev
}

func (r *Redactor) usage(u *llm.Usage) *llm.Usage {
	if u == nil {
		return nil
	}
	c := *u
	c.Raw = r.raw(c.Raw)
	return &c
}
