package protocol

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
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
	r *strings.Replacer // nil when there is nothing to redact
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
	return &Redactor{r: strings.NewReplacer(pairs...)}
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

// String returns s with the credential removed.
func (r *Redactor) String(s string) string {
	if r == nil || r.r == nil {
		return s
	}
	return r.r.Replace(s)
}

// raw returns b with the credential removed, still valid JSON.
func (r *Redactor) raw(b json.RawMessage) json.RawMessage {
	if r == nil || r.r == nil || len(b) == 0 {
		return b
	}
	s := r.r.Replace(string(b))
	if s == string(b) {
		return b
	}
	return llm.RawJSON([]byte(s))
}

// Event returns ev with the credential removed from the text and upstream data it carries:
// error messages and codes, and every raw field. Generated content (deltas, tool names) is the
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
	if ev.Error != nil {
		e := *ev.Error
		e.Message = r.String(e.Message)
		e.ProviderCode = r.String(e.ProviderCode)
		e.Raw = r.raw(e.Raw)
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
