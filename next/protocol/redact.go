package protocol

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Redacted replaces a credential in text that leaves a call.
const Redacted = "[redacted]"

// Redactor removes one call's credential from what the call sends back. A nil Redactor changes
// nothing. Core runs every event an
// extension sends through the Redactor of the call's credential before the event reaches a
// client, a log or anything else, so no provider has to remember to.
type Redactor struct {
	r   *strings.Replacer // nil when there is nothing to redact
	min int               // length of the shortest form redacted; shorter text cannot hold one
}

// NewRedactor returns the Redactor for c. Every string in the credential except its kind counts
// as secret, so a credential kind added later is covered without listing its fields.
func NewRedactor(c *Credential) *Redactor {
	var secrets []string
	if c != nil {
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
	pairs := make([]string, 0, 2*len(variants))
	for _, v := range variants {
		pairs = append(pairs, v, Redacted)
	}
	return &Redactor{r: strings.NewReplacer(pairs...), min: len(variants[len(variants)-1])}
}

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

// String returns s with the credential removed, also where s holds it only once the JSON escapes
// in it are decoded: s may quote a JSON body, as error text often does.
func (r *Redactor) String(s string) string {
	if r == nil || r.r == nil {
		return s
	}
	return r.text(s, 0)
}

// maxDepth bounds how many layers of JSON inside strings are decoded.
const maxDepth = 8

// text returns the decoded string s with the credential removed. A string that is JSON (an
// object, array or string, such as a quoted upstream body) is redacted as JSON in turn, as a
// client parsing it would read it. Escapes that leaves (JSON after other text, escapes nested
// deeper, malformed JSON) can still decode to the credential for a reader; when they do, the
// text is redacted with its escapes decoded.
func (r *Redactor) text(s string, depth int) string {
	if len(s) < r.min {
		return s
	}
	if depth < maxDepth && strings.Contains(s, `\`) {
		if t := strings.TrimSpace(s); t != "" && strings.IndexByte(`{["`, t[0]) >= 0 && json.Valid([]byte(t)) {
			s = string(r.redactJSON([]byte(s), depth+1))
		}
	}
	out := r.r.Replace(s)
	if strings.Contains(out, `\`) {
		if u := unescape(out); r.r.Replace(u) != u {
			return r.r.Replace(u)
		}
	}
	return out
}

var jsonEscape = regexp.MustCompile(`\\(u[0-9a-fA-F]{4}|["\\/bfnrt])`)

// unescape decodes JSON escapes in s, layer after layer, up to maxDepth layers.
func unescape(s string) string {
	for range maxDepth {
		u := jsonEscape.ReplaceAllStringFunc(s, func(m string) string {
			if m[1] == 'u' {
				n, _ := strconv.ParseUint(m[2:], 16, 32)
				return string(rune(n))
			}
			return map[byte]string{'"': `"`, '\\': `\`, '/': "/", 'b': "\b", 'f': "\f", 'n': "\n", 'r': "\r", 't': "\t"}[m[1]]
		})
		if u == s {
			break
		}
		s = u
	}
	return s
}

// raw returns b with the credential removed, still valid JSON. A client decodes the JSON, so
// the credential is removed from each decoded string rather than from b's bytes: an upstream
// can escape any character of it (\u0073 for "s"), and no list of byte encodings covers them
// all.
func (r *Redactor) raw(b json.RawMessage) json.RawMessage {
	if r == nil || r.r == nil || len(b) == 0 {
		return b
	}
	var s string
	switch {
	case !json.Valid(b):
		s = r.r.Replace(string(b))
	case bytes.IndexByte(b, '\\') < 0:
		// Without a backslash every decoded string is its bytes. A replacement that breaks the
		// JSON was in a number.
		if s = r.r.Replace(string(b)); !json.Valid([]byte(s)) {
			s = string(r.redactJSON(b, 0))
		}
	default:
		s = string(r.redactJSON(b, 0))
	}
	if s == string(b) {
		return b
	}
	return llm.RawJSON([]byte(s))
}

// redactJSON returns the valid JSON b with the credential removed from every string in it, object
// keys included, and from every number, which becomes the redacted string. Only tokens that
// change are written again; every other byte is kept.
func (r *Redactor) redactJSON(b []byte, depth int) []byte {
	var out []byte
	last := 0
	for i := 0; i < len(b); i++ {
		if c := b[i]; c == '-' || c >= '0' && c <= '9' {
			end := i
			for end < len(b) && strings.IndexByte("+-.eE0123456789", b[end]) >= 0 {
				end++
			}
			if tok := string(b[i:end]); len(tok) >= r.min && r.r.Replace(tok) != tok {
				out = append(append(out, b[last:i]...), encodeString(Redacted)...)
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
		if tok := b[i : end+1]; len(tok)-2 >= r.min {
			var s string
			if bytes.IndexByte(tok, '\\') < 0 {
				s = string(tok[1 : len(tok)-1])
			} else if json.Unmarshal(tok, &s) != nil {
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
