package configdoc

import (
	"bytes"
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// A redacted document holds a placeholder in place of each secret: PlaceholderPrefix and the
// secret's ID. The ID is a keyed hash of the secret with a key core picks at each start, so it
// tells nothing about the secret, is the same for the same secret while core runs, and changes
// when core restarts.
const PlaceholderPrefix = "pluxy-redacted-"

var placeholder = regexp.MustCompile(`^` + PlaceholderPrefix + `[0-9a-f]{24}$`)

// ErrStale marks a placeholder that matches no secret in the config file as it is now: the
// file changed, or core restarted, since the document was read.
var ErrStale = errors.New("a redacted value matches no secret in the config file as it is now; read the config again")

// Redactor replaces the secrets in config documents with placeholders and back.
type Redactor struct{ key []byte }

// NewRedactor returns a Redactor with a new random key.
func NewRedactor() *Redactor {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return &Redactor{key: key}
}

// ID returns the secret's ID: 24 hex digits of a keyed hash.
func (r *Redactor) ID(secret string) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(secret))
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

// Placeholder returns what a redacted document holds in place of secret.
func (r *Redactor) Placeholder(secret string) string { return PlaceholderPrefix + r.ID(secret) }

// placeholderOf returns the placeholder of a secret's node. In settings, a secret that is not a
// string has its type in its placeholder too, so the number 1234 and the string "1234" have
// different ones and each goes back with its own type. Its hash uses a key of its own, derived
// from the Redactor's, so no string's placeholder is ever a typed value's.
func (r *Redactor) placeholderOf(n *yaml.Node, settings bool) string {
	if !settings || n.Tag == "!!str" {
		return r.Placeholder(n.Value)
	}
	derive := hmac.New(sha256.New, r.key)
	derive.Write([]byte("pluxy settings: a secret that is not a string"))
	mac := hmac.New(sha256.New, derive.Sum(nil))
	fmt.Fprintf(mac, "%d:%s%d:%s", len(n.Tag), n.Tag, len(n.Value), n.Value)
	return PlaceholderPrefix + hex.EncodeToString(mac.Sum(nil)[:12])
}

// The rules that say which values are secrets. They go by key name, so they hold in both
// layouts of a config.yaml and in the JSON config:
//   - the value of a key named like a secret (api-key, secret-key, management_key,
//     client_keys, password, *-token, ...), or each item when it is a list;
//   - every value under a "credential" (an account's credential), except its kind;
//   - the value of a header whose name is about authentication (Authorization, X-Api-Key,
//     Cookie, ...), in any "headers" mapping;
//   - a URL with a user name, password or query, anywhere.
//
// Below a key named like a secret, a list's items that are not mappings are secrets too, however
// deep; a list of mappings (provider entries in a config file) is read by the rules above. Account
// settings (RedactSettings) are stricter: everything below a key named like a secret is one.
var secretNames = map[string]bool{
	"api-key": true, "api-keys": true, "apikey": true, "secret-key": true, "management-key": true,
	"client-keys": true, "password": true, "secret": true, "token": true, "private-key": true,
}

func secretName(key string) bool {
	k := strings.ToLower(strings.ReplaceAll(key, "_", "-"))
	if secretNames[k] {
		return true
	}
	for _, suffix := range []string{"-token", "-secret", "-password", "-api-key"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

func secretHeader(name string) bool {
	h := strings.ToLower(name)
	for _, part := range []string{"auth", "key", "token", "secret", "cookie", "session", "password", "signature"} {
		if strings.Contains(h, part) {
			return true
		}
	}
	return false
}

func secretURL(s string) bool {
	if !strings.Contains(s, "://") {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && u.Host != "" && (u.User != nil || u.RawQuery != "")
}

// secrets returns the scalar nodes of the tree under n that hold secrets. strict: everything
// below a key named like a secret is one (account settings).
func secrets(n *yaml.Node, strict bool) []*yaml.Node {
	var out []*yaml.Node
	add := func(v *yaml.Node) {
		if v.Kind == yaml.ScalarNode && v.Tag != "!!null" && v.Tag != "!!bool" && v.Value != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	// all adds every scalar under v; with kind false, except the values of keys named "kind" (a
	// credential's kind).
	var all func(v *yaml.Node, kind bool)
	all = func(v *yaml.Node, kind bool) {
		switch v.Kind {
		case yaml.ScalarNode:
			add(v)
		case yaml.AliasNode:
			if v.Alias != nil {
				all(v.Alias, kind)
			}
		case yaml.SequenceNode:
			for _, c := range v.Content {
				all(c, kind)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(v.Content); i += 2 {
				if kind || v.Content[i].Value != "kind" {
					all(v.Content[i+1], kind)
				}
			}
		}
	}
	var walk func(v *yaml.Node)
	walk = func(v *yaml.Node) {
		switch v.Kind {
		case yaml.ScalarNode:
			if secretURL(v.Value) {
				add(v)
			}
		case yaml.SequenceNode:
			for _, c := range v.Content {
				walk(c)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(v.Content); i += 2 {
				key, val := v.Content[i].Value, v.Content[i+1]
				switch {
				case strings.EqualFold(key, "credential"):
					all(val, false)
				case strings.EqualFold(key, "headers") && val.Kind == yaml.MappingNode:
					for j := 0; j+1 < len(val.Content); j += 2 {
						if secretHeader(val.Content[j].Value) {
							all(val.Content[j+1], true)
						} else {
							walk(val.Content[j+1])
						}
					}
				case secretName(key) && (strict || val.Kind == yaml.ScalarNode || val.Kind == yaml.AliasNode):
					all(val, true)
				case secretName(key) && val.Kind == yaml.SequenceNode:
					// A list of keys, or of lists of keys; a list of mappings (provider key
					// entries) is walked.
					for _, c := range val.Content {
						if c.Kind != yaml.MappingNode {
							all(c, true)
						} else {
							walk(c)
						}
					}
				default:
					walk(val)
				}
			}
		}
		// An alias's node is walked where its anchor is.
	}
	walk(n)
	return out
}

// Secrets returns the secret values the document holds.
func (d *Doc) Secrets() []string {
	var out []string
	for _, n := range secrets(d.root, false) {
		out = append(out, n.Value)
	}
	return out
}

// Redact returns a config file's contents with a placeholder in place of each secret. The
// rest stays as written, comments included. Where a secret's text cannot be found exactly (a
// block scalar, an anchor or tag before it), the document is written out again with the
// placeholders, as Doc.Bytes writes it.
func (r *Redactor) Redact(b []byte, isYAML bool) ([]byte, error) {
	return r.redact(b, isYAML, false)
}

// RedactSettings returns an account's settings, a JSON object, with a placeholder in place of
// each secret, by the stricter rules for settings: everything below a key named like a secret is
// a secret, however deep.
func (r *Redactor) RedactSettings(b []byte) ([]byte, error) { return r.redact(b, false, true) }

func (r *Redactor) redact(b []byte, isYAML, strict bool) ([]byte, error) {
	d, err := Parse(b, isYAML)
	if err != nil {
		return nil, err
	}
	found := secrets(d.root, strict)
	if len(found) == 0 {
		return b, nil
	}
	repl := map[*yaml.Node]string{}
	for _, n := range found {
		p := r.placeholderOf(n, strict)
		if !isYAML {
			p = `"` + p + `"`
		}
		repl[n] = p
	}
	var values []string
	for _, n := range found {
		values = append(values, n.Value)
	}
	if out, ok := d.replace(repl); ok && redacted(out, isYAML, strict, values) {
		return out, nil
	}
	for _, n := range found {
		setScalar(n, "!!str", r.placeholderOf(n, strict), 0)
	}
	out, err := d.encode()
	if err != nil {
		return nil, err
	}
	if !redacted(out, isYAML, strict, values) {
		return nil, errors.New("config: could not redact the config file's secrets")
	}
	return out, nil
}

// setScalar replaces a scalar's value, keeping its anchor (aliases may refer to it) and its
// comments.
func setScalar(n *yaml.Node, tag, value string, style yaml.Style) {
	*n = yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value, Style: style, Anchor: n.Anchor,
		HeadComment: n.HeadComment, LineComment: n.LineComment, FootComment: n.FootComment}
}

// redacted reports whether a document holds none of the secret values, as a value, and every
// value in it that the rules call a secret is a placeholder.
func redacted(b []byte, isYAML, strict bool, values []string) bool {
	d, err := Parse(b, isYAML)
	if err != nil {
		return false
	}
	for _, n := range secrets(d.root, strict) {
		if !placeholder.MatchString(n.Value) {
			return false
		}
	}
	clean := true
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && slices.Contains(values, n.Value) {
			clean = false
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(d.root)
	return clean
}

// Restore returns an edited document with each placeholder replaced by the secret it stands
// for in current, the config file as it is now. A placeholder may only be where a secret
// goes; one that matches no secret in current is an ErrStale. A document without placeholders
// comes back as it is.
func (r *Redactor) Restore(edited, current []byte, isYAML bool) ([]byte, error) {
	return r.restore(edited, current, isYAML, false)
}

// RestoreSettings is Restore for an account's settings, a JSON object, by the rules of
// RedactSettings: current is the account's settings as they are now.
func (r *Redactor) RestoreSettings(edited, current []byte) ([]byte, error) {
	return r.restore(edited, current, false, true)
}

func (r *Redactor) restore(edited, current []byte, isYAML, settings bool) ([]byte, error) {
	d, err := Parse(edited, isYAML)
	if err != nil {
		return nil, err
	}
	var held []*yaml.Node
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && placeholder.MatchString(n.Value) {
			held = append(held, n)
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(d.root)
	if len(held) == 0 {
		return edited, nil
	}
	// Each placeholder goes back as the secret's text in current, so it keeps its quotes, or
	// in JSON its type. In settings, it goes back only where the secret is in current (paths).
	type secret struct {
		value, tag, text string
		paths            map[string]bool
	}
	values := map[string]*secret{}
	if cur, err := Parse(current, isYAML); err == nil {
		where := paths(cur.root)
		for _, n := range secrets(cur.root, settings) {
			p := r.placeholderOf(n, settings)
			s := values[p]
			if s == nil {
				s = &secret{value: n.Value, tag: n.Tag, paths: map[string]bool{}}
				if sp, ok := cur.span(n); ok {
					s.text = string(cur.src[sp.start:sp.end])
				}
				values[p] = s
			}
			s.paths[where[n]] = true
		}
	}
	allowed := secrets(d.root, settings)
	var where map[*yaml.Node]string
	if settings {
		where = paths(d.root)
	}
	repl := map[*yaml.Node]*secret{}
	for _, n := range held {
		s, ok := values[n.Value]
		switch {
		case settings && ok && !s.paths[where[n]]:
			return nil, fmt.Errorf("%w: line %d: a redacted value is not where its secret is; write the value itself there", ErrInvalid, d.line(n))
		case settings && !slices.Contains(allowed, n),
			// A URL that is a secret is one wherever it is, so in a config file its placeholder
			// may go anywhere.
			!settings && !slices.Contains(allowed, n) && !(ok && secretURL(s.value)):
			return nil, fmt.Errorf("%w: line %d: a redacted value is where no secret goes; write the value itself there", ErrInvalid, d.line(n))
		case !ok:
			return nil, fmt.Errorf("line %d: %w", d.line(n), ErrStale)
		}
		repl[n] = s
	}
	want := map[string]int{}
	texts := map[*yaml.Node]string{}
	for n, s := range repl {
		want[s.value]++
		switch {
		case s.text != "":
			texts[n] = s.text
		case isYAML && plain(s.value):
			texts[n] = s.value
		default:
			texts[n] = jsonString(s.value)
		}
	}
	if out, ok := d.replace(texts); ok && restored(out, isYAML, settings, want) {
		return out, nil
	}
	for n, s := range repl {
		style := yaml.Style(0)
		if isYAML && s.tag == "!!str" && !plain(s.value) {
			style = yaml.DoubleQuotedStyle
		}
		setScalar(n, s.tag, s.value, style)
	}
	out, err := d.encode()
	if err != nil {
		return nil, err
	}
	if !restored(out, isYAML, settings, want) {
		return nil, fmt.Errorf("%w: could not put the redacted secrets back", ErrInvalid)
	}
	return out, nil
}

// paths returns where each node under root is: the keys and list indexes leading to it. A list
// item that is a scalar has [] for its index, so it may move within its own list, and only there.
func paths(root *yaml.Node) map[*yaml.Node]string {
	out := map[*yaml.Node]string{}
	var walk func(n *yaml.Node, at string)
	walk = func(n *yaml.Node, at string) {
		out[n] = at
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i+1], at+"."+strconv.Quote(n.Content[i].Value))
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				if c.Kind == yaml.ScalarNode {
					walk(c, at+"[]")
				} else {
					walk(c, at+"["+strconv.Itoa(i)+"]")
				}
			}
		}
	}
	walk(root, "")
	return out
}

// restored reports whether a document holds no placeholder and holds each wanted secret value
// at least as many times as wanted.
func restored(b []byte, isYAML, strict bool, want map[string]int) bool {
	d, err := Parse(b, isYAML)
	if err != nil {
		return false
	}
	have := map[string]int{}
	for _, n := range secrets(d.root, strict) {
		if placeholder.MatchString(n.Value) {
			return false
		}
		have[n.Value]++
	}
	for v, n := range want {
		if have[v] < n {
			return false
		}
	}
	return true
}

// plain reports whether s can be written as a plain YAML scalar that reads back as the same
// string, in a block or a flow collection.
func plain(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n#:,[]{}&*!|>'\"%@`") || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "?") {
		return false
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil || len(n.Content) != 1 {
		return false
	}
	v := n.Content[0]
	return v.Kind == yaml.ScalarNode && v.Tag == "!!str" && v.Value == s
}

// replace returns the source with each node's text replaced; ok is false when a node's text
// cannot be found exactly.
func (d *Doc) replace(repl map[*yaml.Node]string) ([]byte, bool) {
	type edit struct {
		span
		text string
	}
	var edits []edit
	for n, text := range repl {
		s, ok := d.span(n)
		if !ok {
			return nil, false
		}
		edits = append(edits, edit{s, text})
	}
	slices.SortFunc(edits, func(a, b edit) int { return b.start - a.start })
	out := slices.Clone(d.src)
	for _, e := range edits {
		out = slices.Concat(out[:e.start], []byte(e.text), out[e.end:])
	}
	return out, true
}

// span returns where a scalar's text is in the source, quotes included.
func (d *Doc) span(n *yaml.Node) (span, bool) {
	if !d.YAML {
		s, ok := d.spans[n]
		return s, ok
	}
	if n.Kind != yaml.ScalarNode || n.Anchor != "" || n.Line < 1 || n.Column < 1 {
		return span{}, false
	}
	start := offset(d.src, n.Line, n.Column)
	if start < 0 {
		return span{}, false
	}
	src := d.src
	switch n.Style {
	case yaml.DoubleQuotedStyle:
		if src[start] != '"' {
			return span{}, false
		}
		for i := start + 1; i < len(src); i++ {
			switch src[i] {
			case '\\':
				i++
			case '"':
				return span{start, i + 1}, true
			}
		}
	case yaml.SingleQuotedStyle:
		if src[start] != '\'' {
			return span{}, false
		}
		for i := start + 1; i < len(src); i++ {
			if src[i] == '\'' {
				if i+1 < len(src) && src[i+1] == '\'' {
					i++
					continue
				}
				return span{start, i + 1}, true
			}
		}
	case 0:
		if !strings.Contains(n.Value, "\n") && bytes.HasPrefix(src[start:], []byte(n.Value)) {
			return span{start, start + len(n.Value)}, true
		}
	}
	return span{}, false
}

// offset returns the byte offset of a 1-based line and column (in characters, as yaml.v3
// counts them), or -1.
func offset(src []byte, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(src[off:], '\n')
		if i < 0 {
			return -1
		}
		off += i + 1
	}
	for c := 1; c < col; c++ {
		if off >= len(src) || src[off] == '\n' {
			return -1
		}
		_, size := utf8.DecodeRune(src[off:])
		off += size
	}
	if off >= len(src) {
		return -1
	}
	return off
}

// line returns the line a node is on, for messages.
func (d *Doc) line(n *yaml.Node) int {
	if d.YAML {
		return n.Line
	}
	return 1 + bytes.Count(d.src[:d.spans[n].start], []byte("\n"))
}

// Scrub returns msg with each of the secrets in it replaced by its placeholder, longest
// first. A very short secret may garble the message; showing it would be worse. Secrets are
// looked for in msg as it was given, never in a placeholder put in it, so a short secret
// (such as "red" or "abc") cannot alter the placeholder of another.
func (r *Redactor) Scrub(msg string, secrets []string) string {
	secrets = slices.DeleteFunc(slices.Clone(secrets), func(s string) bool { return s == "" })
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	secrets = slices.Compact(secrets)
	type found struct {
		start, end int
		secret     string
	}
	var hits []found
	for _, s := range secrets {
		for from := 0; from < len(msg); {
			i := strings.Index(msg[from:], s)
			if i < 0 {
				break
			}
			start, end := from+i, from+i+len(s)
			// A longer secret that already holds this text keeps it.
			if slices.ContainsFunc(hits, func(h found) bool { return start < h.end && h.start < end }) {
				from = start + 1
				continue
			}
			hits = append(hits, found{start, end, s})
			from = end
		}
	}
	slices.SortFunc(hits, func(a, b found) int { return a.start - b.start })
	var out strings.Builder
	last := 0
	for _, h := range hits {
		out.WriteString(msg[last:h.start])
		out.WriteString(r.Placeholder(h.secret))
		last = h.end
	}
	out.WriteString(msg[last:])
	return out.String()
}
