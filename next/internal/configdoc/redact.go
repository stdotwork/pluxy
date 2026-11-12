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

	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/upheader"
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

// placeholderOf returns the placeholder of a secret's node. A secret that is not a string has its
// type in its placeholder too, so the number 1234 and the string "1234" have different ones and
// each goes back with its own type, wherever its placeholder is moved. Its hash uses a key of its
// own, derived from the Redactor's, so no string's placeholder is ever a typed value's. A secret
// header value of a provider section has the placeholder of the header core reads from it
// (texts, see found), the one Headers shows for that account.
func (r *Redactor) placeholderOf(n *yaml.Node, texts map[*yaml.Node]string) string {
	if t, ok := texts[n]; ok {
		return r.Placeholder(t)
	}
	if n.Tag == "!!str" {
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
// Everything below a key named like a secret is a secret too, however deep, except in the provider
// sections today's core reads at the top of a config.yaml: the v8 api-keys mapping and the older
// lists of entries (codex-api-key and the rest, legacy.SectionLists), whose entries are read by
// the rules above. Account settings have no such sections, on their own (RedactSettings) or as
// the "settings" of each item of an "accounts" list in a config file. Everywhere, true, false,
// null and the empty string are never secrets: they carry none, and hiding them would hide what a
// setting is.
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

func secretHeader(name string) bool { return upheader.Secret(name) }

// Headers returns an account's headers fit to show: the placeholder a redacted config document
// holds in place of each secret value (upheader.Secret).
func (r *Redactor) Headers(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if upheader.Secret(k) {
			v = r.Placeholder(v)
		}
		out[k] = v
	}
	return out
}

// RestoreHeaders returns sent, headers sent back for an account, with each placeholder that
// Headers showed for a secret value of cur, the account's headers now, replaced by that value.
// A placeholder may only be the value of a secret header, and one that stands for no secret
// value of cur is refused, so the placeholder itself is never stored.
func (r *Redactor) RestoreHeaders(sent, cur map[string]string) (map[string]string, error) {
	values := map[string]string{}
	for _, v := range upheader.Secrets(cur) {
		values[r.Placeholder(v)] = v
	}
	var out map[string]string
	for k, v := range sent {
		if p := strings.TrimSpace(v); placeholder.MatchString(p) {
			if !upheader.Secret(k) {
				return nil, fmt.Errorf("header %s holds a redacted value, but its value is not secret; write the value itself", k)
			}
			was, ok := values[p]
			if !ok {
				return nil, fmt.Errorf("header %s holds a redacted value that stands for none of the account's header values now; read the account again", k)
			}
			v = was
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out, nil
}

func secretURL(s string) bool {
	if !strings.Contains(s, "://") {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && u.Host != "" && (u.User != nil || u.RawQuery != "")
}

// sectionLists are the top-level keys of a config.yaml whose value is a list of provider entries.
var sectionLists = legacy.SectionLists()

// ctx is how secrets reads a node: what the place it is in makes it, and whether it is in a
// provider section.
type ctx struct {
	role    role
	section bool
}

// role is what a node's place makes it, for the rules that look at a value.
type role int

const (
	rolePlain    role = iota
	roleTop           // a config file's top-level mapping
	roleSecret        // the value of a key named like a secret
	roleSections      // the v8 api-keys mapping, at the top of a config file
	roleEntries       // an older list of provider entries, at the top of a config file
	roleHeaders       // the value of a "headers" key
)

// resolved returns the node an alias stands for, or n itself.
func resolved(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

// keyName returns the name a mapping key node gives, also when the key is an alias of an
// anchored name (*k : value), as core reads it.
func keyName(n *yaml.Node) string { return resolved(n).Value }

// secrets returns the scalar nodes of the tree under n that hold secrets. strict: n is an
// account's settings, not a config file, so it has no provider sections.
func secrets(n *yaml.Node, strict bool) []*yaml.Node {
	out, _ := found(n, strict)
	return out
}

// found is secrets, with the text core reads from each secret header value of a provider section
// (legacy.HeaderText), which core reads as text without the spaces around it.
func found(n *yaml.Node, strict bool) (out []*yaml.Node, texts map[*yaml.Node]string) {
	// add records v as a secret, unless it is true, false, null or empty (see the rules above).
	add := func(v *yaml.Node) {
		if v.Kind == yaml.ScalarNode && v.Tag != "!!null" && v.Tag != "!!bool" && v.Value != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	// all adds every scalar under v; with kind false, except the values of keys named "kind" (a
	// credential's kind). Each node is read at most once in each way, so an alias back into its
	// own anchor, or anchors that nest aliases many times over, cost no more than the document's
	// size.
	type allReading struct {
		n    *yaml.Node
		kind bool
	}
	allRead := map[allReading]bool{}
	var all func(v *yaml.Node, kind bool)
	all = func(v *yaml.Node, kind bool) {
		k := allReading{v, kind}
		if allRead[k] {
			return
		}
		allRead[k] = true
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
				if kind || keyName(v.Content[i]) != "kind" {
					all(v.Content[i+1], kind)
				}
			}
		}
	}
	// walk reads the tree under v by the rules above, in the way c says the place v is in reads
	// it: v may be a config file's top-level mapping or the value of a key a rule names (headers,
	// a secret key, a provider section). An alias is read as its anchor would be in the alias's
	// place, whatever the anchor's own place, and a mapping merged with "<<" is
	// read as part of the mapping it is merged into. Each anchor is read at most once in each
	// way, so an alias back into its own anchor, or anchors that nest aliases many times over,
	// cost no more than the document's size.
	type reading struct {
		n *yaml.Node
		c ctx
	}
	read := map[reading]bool{}
	var walk func(v *yaml.Node, c ctx)
	// pairs reads the key-value pairs of the mapping m, and those of the mappings merged into it.
	var pairs func(m *yaml.Node, c ctx, each func(key string, val *yaml.Node))
	pairs = func(m *yaml.Node, c ctx, each func(key string, val *yaml.Node)) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			key, val := m.Content[i], m.Content[i+1]
			if key.Value != "<<" || key.Tag != "!!merge" {
				each(keyName(key), val)
				continue
			}
			merged := []*yaml.Node{val}
			if r := resolved(val); r.Kind == yaml.SequenceNode {
				merged = r.Content
			}
			for _, x := range merged {
				if r := resolved(x); r.Kind == yaml.MappingNode && !read[reading{r, c}] {
					read[reading{r, c}] = true
					pairs(r, c, each)
				}
			}
		}
	}
	walk = func(v *yaml.Node, c ctx) {
		if v.Kind == yaml.AliasNode {
			if k := (reading{v.Alias, c}); v.Alias != nil && !read[k] {
				read[k] = true
				walk(v.Alias, c)
			}
			return
		}
		plain := ctx{section: c.section}
		switch {
		case c.role == roleSecret:
			all(v, true)
			return
		case c.role == roleSections && v.Kind == yaml.MappingNode:
			// Each section is a list of provider entries, read by the rules.
			pairs(v, c, func(_ string, val *yaml.Node) { walk(val, ctx{section: true}) })
			return
		case c.role == roleEntries && v.Kind == yaml.SequenceNode:
			// An entry is read by the rules; anything else in the list is a key.
			for _, x := range v.Content {
				if resolved(x).Kind != yaml.MappingNode {
					all(x, true)
				} else {
					walk(x, ctx{section: true})
				}
			}
			return
		case c.role == roleSections || c.role == roleEntries:
			all(v, true)
			return
		case c.role == roleHeaders && v.Kind == yaml.MappingNode:
			pairs(v, c, func(name string, val *yaml.Node) {
				if secretHeader(name) {
					if s := resolved(val); c.section && s.Kind == yaml.ScalarNode {
						var v any
						if s.Decode(&v) == nil {
							if t, ok := legacy.HeaderText(v); ok {
								if texts == nil {
									texts = map[*yaml.Node]string{}
								}
								texts[s] = t
							}
						}
					}
					all(val, true)
				} else {
					walk(val, plain)
				}
			})
			return
		}
		switch v.Kind {
		case yaml.ScalarNode:
			if secretURL(v.Value) {
				add(v)
			}
		case yaml.SequenceNode:
			for _, x := range v.Content {
				walk(x, plain)
			}
		case yaml.MappingNode:
			pairs(v, c, func(key string, val *yaml.Node) {
				switch {
				case strings.EqualFold(key, "credential"):
					all(val, false)
				case strings.EqualFold(key, "headers"):
					walk(val, ctx{role: roleHeaders, section: c.section})
				case c.role == roleTop && key == "api-keys" && resolved(val).Kind == yaml.MappingNode:
					walk(val, ctx{role: roleSections})
				case c.role == roleTop && slices.Contains(sectionLists, key):
					walk(val, ctx{role: roleEntries})
				case secretName(key):
					walk(val, ctx{role: roleSecret, section: c.section})
				default:
					walk(val, plain)
				}
			})
		}
	}
	if strict {
		walk(n, ctx{})
	} else {
		walk(n, ctx{role: roleTop})
	}
	return out, texts
}

// Secrets returns the secret values the document holds.
func (d *Doc) Secrets() []string {
	var out []string
	for _, n := range secrets(d.root, false) {
		out = append(out, n.Value)
	}
	return out
}

// MinTextSecret is the length, in bytes, below which SettingsSecrets leaves a secret out: removing
// every occurrence of a string that short from a reply or a log would mangle unrelated text. A
// placeholder in a settings document replaces the whole value, so it covers short ones too.
const MinTextSecret = 8

// SettingsSecrets returns the secret values an account's settings, a JSON object, hold by the
// rules of RedactSettings, to be removed from text where they occur: those of MinTextSecret
// bytes or more. Of a URL, its parts are returned on their own too, as an extension may write
// one without the rest: the forms of its user name and password a proxy URL's are removed in
// (proxyurl.Password; the redaction adds the Basic form), and each query value. Settings that
// do not parse hold none it can find.
func SettingsSecrets(b []byte) []string {
	d, err := Parse(b, false)
	if err != nil {
		return nil
	}
	var out []string
	add := func(s string) {
		if len(s) >= MinTextSecret && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, n := range secrets(d.root, true) {
		add(n.Value)
		if !secretURL(n.Value) {
			continue
		}
		for _, f := range proxyurl.Password(n.Value) {
			add(f)
		}
		if u, err := url.Parse(strings.TrimSpace(n.Value)); err == nil {
			for _, vs := range u.Query() {
				for _, v := range vs {
					add(v)
				}
			}
		}
	}
	return out
}

// Redact returns a config file's contents with a placeholder in place of each secret. The
// rest stays as written, comments included. Where a secret's text cannot be found exactly (a
// block scalar, an anchor before it), the document is written out again with the placeholders,
// as Doc.Bytes writes it.
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
	nodes, texts := found(d.root, strict)
	if len(nodes) == 0 {
		return b, nil
	}
	repl := map[*yaml.Node]string{}
	for _, n := range nodes {
		p := r.placeholderOf(n, texts)
		if !isYAML {
			p = `"` + p + `"`
		}
		repl[n] = p
	}
	// A secret value may also be written where it is no secret (a 3 below a secret key and a
	// retry count of 3): the redacted document may show it as many times as it is written so.
	values := map[string]bool{}
	for _, n := range nodes {
		values[n.Value] = true
	}
	shown := map[string]int{}
	eachScalar(d.root, func(n *yaml.Node) {
		if _, secret := repl[n]; !secret && values[n.Value] {
			shown[n.Value]++
		}
	})
	if out, ok := d.replace(repl); ok && redacted(out, isYAML, strict, values, shown) {
		return out, nil
	}
	for _, n := range nodes {
		setScalar(n, "!!str", r.placeholderOf(n, texts), 0)
	}
	out, err := d.encode()
	if err != nil {
		return nil, err
	}
	if !redacted(out, isYAML, strict, values, shown) {
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

// redacted reports whether every value in a document that the rules call a secret is a
// placeholder, and it holds each of the secret values as a value no more times than shown says
// the value is written where it is no secret.
func redacted(b []byte, isYAML, strict bool, values map[string]bool, shown map[string]int) bool {
	d, err := Parse(b, isYAML)
	if err != nil {
		return false
	}
	for _, n := range secrets(d.root, strict) {
		if !placeholder.MatchString(n.Value) {
			return false
		}
	}
	seen := map[string]int{}
	eachScalar(d.root, func(n *yaml.Node) {
		if values[n.Value] {
			seen[n.Value]++
		}
	})
	for v, n := range seen {
		if n > shown[v] {
			return false
		}
	}
	return true
}

// eachScalar calls f with each scalar node of the tree under n, once each (an alias's anchor is
// where it is written).
func eachScalar(n *yaml.Node, f func(*yaml.Node)) {
	if n.Kind == yaml.ScalarNode {
		f(n)
	}
	for _, c := range n.Content {
		eachScalar(c, f)
	}
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
	// Two secrets of current may share a placeholder (a header's 1234 and "1234" are the same
	// header text): a placeholder goes back as the one where it is, else as the first.
	type secret struct {
		value, tag, text string
		tagged           bool               // written with an explicit tag, which must go back with it
		paths            map[string]*secret // where the placeholder's secrets are, and which is there
	}
	values := map[string]*secret{}
	if cur, err := Parse(current, isYAML); err == nil {
		where := paths(cur.root)
		nodes, texts := found(cur.root, settings)
		for _, n := range nodes {
			p := r.placeholderOf(n, texts)
			this := &secret{value: n.Value, tag: n.Tag, tagged: n.Style&yaml.TaggedStyle != 0}
			if sp, ok := cur.span(n); ok {
				this.text = string(cur.src[sp.start:sp.end])
			}
			s := values[p]
			if s == nil {
				s = this
				s.paths = map[string]*secret{}
				values[p] = s
			}
			if s.paths[where[n]] == nil {
				s.paths[where[n]] = this
			}
		}
	}
	allowed := secrets(d.root, settings)
	where := paths(d.root)
	repl := map[*yaml.Node]*secret{}
	for _, n := range held {
		s, ok := values[n.Value]
		switch {
		case settings && ok && s.paths[where[n]] == nil:
			return nil, fmt.Errorf("%w: line %d: a redacted value is not where its secret is; write the value itself there", ErrInvalid, d.line(n))
		case settings && !slices.Contains(allowed, n),
			// A URL that is a secret is one wherever it is, so in a config file its placeholder
			// may go anywhere.
			!settings && !slices.Contains(allowed, n) && !(ok && secretURL(s.value)):
			return nil, fmt.Errorf("%w: line %d: a redacted value is where no secret goes; write the value itself there", ErrInvalid, d.line(n))
		case !ok:
			return nil, fmt.Errorf("line %d: %w", d.line(n), ErrStale)
		}
		if at := s.paths[where[n]]; at != nil {
			s = at
		}
		repl[n] = s
	}
	want := map[string]int{}
	texts := map[*yaml.Node]string{}
	inPlace := true
	for n, s := range repl {
		want[s.value]++
		switch {
		case s.text != "":
			texts[n] = s.text
		case s.tagged:
			// Only writing the document out again keeps a tag whose text was not found.
			inPlace = false
		case isYAML && plain(s.value):
			texts[n] = s.value
		default:
			texts[n] = jsonString(s.value)
		}
	}
	if inPlace {
		if out, ok := d.replace(texts); ok && restored(out, isYAML, settings, want) {
			return out, nil
		}
	}
	for n, s := range repl {
		style := yaml.Style(0)
		switch {
		case s.tagged:
			style = yaml.TaggedStyle
		case isYAML && s.tag == "!!str" && !plain(s.value):
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
				walk(n.Content[i+1], at+"."+strconv.Quote(keyName(n.Content[i])))
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

// span returns where a scalar's text is in the source, quotes included, and an explicit tag
// before it (!!int 0x1F) too.
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
	style := n.Style
	if style&yaml.TaggedStyle != 0 {
		// The node's place is its tag's: the value follows it on the same line.
		style &^= yaml.TaggedStyle
		i := start
		for i < len(src) && src[i] != ' ' && src[i] != '\t' && src[i] != '\n' {
			i++
		}
		if src[start] != '!' || i == len(src) || src[i] == '\n' {
			return span{}, false
		}
		for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
			i++
		}
		v, ok := valueSpan(src, i, n.Value, style)
		return span{start, v.end}, ok
	}
	return valueSpan(src, start, n.Value, style)
}

// valueSpan returns where the text of a scalar of the given value and style that starts at start
// ends in src.
func valueSpan(src []byte, start int, value string, style yaml.Style) (span, bool) {
	if start >= len(src) {
		return span{}, false
	}
	switch style {
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
		if !strings.Contains(value, "\n") && bytes.HasPrefix(src[start:], []byte(value)) {
			return span{start, start + len(value)}, true
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
// first. A secret shorter than MinTextSecret is left in msg, as substring redaction leaves it:
// replacing every occurrence of so short a string would garble the message (a retry count of 3
// in "out of range 1-3"). Secrets are looked for in msg as it was given, never in a placeholder
// put in it, so one secret cannot alter the placeholder of another.
func (r *Redactor) Scrub(msg string, secrets []string) string {
	secrets = slices.DeleteFunc(slices.Clone(secrets), func(s string) bool { return len(s) < MinTextSecret })
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
