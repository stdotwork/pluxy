package protocol

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
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

// span is where in the text being redacted some decoded bytes came from: text[start:end].
type span struct{ start, end int }

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
		last = c.end
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
			rawStart = spans[p-1].end
		}
		if q > 0 {
			rawEnd = spans[len(s)-q].start
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
	codes = set(llm.CodeInvalidRequest, llm.CodeAuth, llm.CodePermission, llm.CodeNotFound, llm.CodeRateLimit,
		llm.CodeQuota, llm.CodeUnsupported, llm.CodeUpstream, llm.CodeUnavailable, llm.CodeCancelled, llm.CodeInternal,
		llm.CodeExtensionCrashed, llm.CodeExtensionProtocol, llm.CodeExtensionUnavailable)
	scopes   = set(llm.ScopeRequest, llm.ScopeCredential, llm.ScopeModel, llm.ScopeEndpoint)
	finishes = set(llm.FinishStop, llm.FinishLength, llm.FinishToolCalls, llm.FinishContentFilter, llm.FinishOther)
	formats  = set(llm.FormatOpenAIChat, llm.FormatOpenAIResponses, llm.FormatAnthropicMessages, llm.FormatGeminiGenerate,
		llm.FormatOpenAIResponsesCompact, llm.FormatCodexAlphaSearch, llm.FormatGeminiInteractions,
		llm.FormatOpenAIImagesGenerations, llm.FormatOpenAIImagesEdits, llm.OpaqueDevin)
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
	if ev.Error != nil {
		e := *ev.Error
		e.Code, e.Scope = r.field(e.Code, codes), r.field(e.Scope, scopes)
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
