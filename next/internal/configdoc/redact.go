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
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/protocol"
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

// The rules that say which values are secrets. They go by key name, compared in lower case
// whatever joins its words, or none (accessToken, access_token, access-token and accesstoken
// are one name), with letters folded as encoding/json folds them and a trailing number or word
// saying how the value is written left out (apiKey2, api_key_value), so they hold in both
// layouts of a config.yaml, in the JSON config and in settings:
//   - the value of a key named like a secret (api-key, secret-key, management_key,
//     client_keys, password, *-token, *_secret_access_key, ...), or each item when it is a list;
//   - every value under a "credential" (an account's credential), except its kind;
//   - the value of a header whose name is about authentication (Authorization, X-Api-Key,
//     Cookie, ...) or is named like a secret, in any mapping whose name holds header
//     (headers, extra_headers, headers_override); a mapping there is read by these rules;
//   - a URL with a user name, password or query, anywhere;
//   - a copy: a string of MinTextSecret bytes or more, written as a whole value, that is also
//     the value of a secret by one of the rules above by itself (not only for being in a
//     mapping below a secret key), as note: <the key> (see found).
//
// Everything below a key named like a secret is a secret too, however deep, except in the provider
// sections today's core reads at the top of a config.yaml: the sections of the v8 api-keys
// mapping (legacy.SectionNames; any other key there is read as a secret key) and the older lists
// of entries (codex-api-key and the rest, legacy.SectionLists), whose entries are read by the
// rules above. Account settings have no such sections, on their own (RedactSettings) or as
// the "settings" of each item of an "accounts" list in a config file. Everywhere, true, false,
// null and the empty string are never secrets: they carry none, and hiding them would hide what a
// setting is.
var secretNames = map[string]bool{
	"apikeys": true, "managementkey": true, "clientkeys": true,
}

// secretSuffixes end the other names of secrets, and are names of secrets themselves:
// refreshtoken, clientsecret, dbpassword, awssecretaccesskey, ...
var secretSuffixes = []string{"token", "secret", "secrets", "password", "passwords", "apikey", "secretkey",
	"privatekey", "secretaccesskey", "passphrase"}

// spellingWords are words a secret's name may end in that say how its value is written:
// api_key_value, privateKeyPem, client_secret_b64. A number may end it too (apiKey2).
var spellingWords = []string{"value", "pem", "der", "data", "raw", "b64", "base64", "hex", "str", "string", "text", "bytes"}

func secretName(key string) bool {
	w := words(key)
	for len(w) > 0 {
		last := w[len(w)-1]
		if len(w) > 1 && (slices.Contains(spellingWords, last) || strings.Trim(last, "0123456789") == "") {
			w = w[:len(w)-1]
			continue
		}
		if t := strings.TrimRight(last, "0123456789"); t != "" {
			w[len(w)-1] = t
		}
		break
	}
	k := strings.Join(w, "")
	if secretNames[k] {
		return true
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// headersName reports whether a key names a mapping of HTTP headers: headers, extra_headers,
// customHeaders, extra_header, headers_override, ...
func headersName(key string) bool { return strings.Contains(joinedName(key), "header") }

// joinedName returns a key name in lower case with its words written together: accessToken,
// ACCESS_TOKEN, access.token, access-token and accesstoken are all accesstoken.
func joinedName(key string) string { return strings.Join(words(key), "") }

// words returns the words of a key name, in lower case: those between _, -, . and spaces, and
// those camelCase starts (apiKey2 is api, key2; APIKey is api, key). Letters are folded as
// encoding/json folds them to match a key to a field (fold), so client_keyſ is client_keys, as
// core loads a JSON config.
func words(key string) []string {
	r := []rune(key)
	var out []string
	from := -1
	for i, c := range r {
		if c == '_' || c == '-' || c == '.' || c == ' ' {
			if from >= 0 {
				out = append(out, fold(r[from:i]))
			}
			from = -1
			continue
		}
		if from >= 0 && i > 0 {
			prev := r[i-1]
			upper := unicode.IsUpper(c) && (unicode.IsLower(prev) || unicode.IsDigit(prev) ||
				unicode.IsUpper(prev) && i+1 < len(r) && unicode.IsLower(r[i+1]))
			if upper {
				out = append(out, fold(r[from:i]))
				from = i
			}
		}
		if from < 0 {
			from = i
		}
	}
	if from >= 0 {
		out = append(out, fold(r[from:]))
	}
	return out
}

// fold returns a word in lower case, each letter first folded as encoding/json does when it
// matches a key to a field: to the smallest letter it is a case of (unicode.SimpleFold), so ſ
// (U+017F) is s and the Kelvin sign is k.
func fold(word []rune) string {
	out := make([]rune, len(word))
	for i, c := range word {
		for {
			f := unicode.SimpleFold(c)
			if f <= c {
				c = f
				break
			}
			c = f
		}
		out[i] = unicode.ToLower(c)
	}
	return string(out)
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

// sectionLists are the top-level keys of a config.yaml whose value is a list of provider entries,
// and sectionNames the keys of its v8 api-keys mapping that are.
var sectionLists, sectionNames = legacy.SectionLists(), legacy.SectionNames()

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
	out, _, _ := found(n, strict)
	return out
}

// found is secrets, with the text core reads from each secret header value of a provider section
// (legacy.HeaderText), which core reads as text without the spaces around it, and the secrets
// that are ones only for being in a mapping below a secret key (nested: the 3 of
// client_secret: {rotate_after_days: 3}), not a secret key's value or one of its list's items.
// Its secrets include the copies of secrets (see the rules above), last.
func found(n *yaml.Node, strict bool) (out []*yaml.Node, texts map[*yaml.Node]string, nested map[*yaml.Node]bool) {
	// add records v as a secret, unless it is true, false, null or empty (see the rules above).
	added, direct := map[*yaml.Node]bool{}, map[*yaml.Node]bool{}
	add := func(v *yaml.Node, inMapping bool) {
		if v.Kind == yaml.ScalarNode && v.Tag != "!!null" && v.Tag != "!!bool" && v.Value != "" {
			if !added[v] {
				added[v] = true
				out = append(out, v)
			}
			if !inMapping {
				direct[v] = true
			}
		}
	}
	// all adds every scalar under v; with kind false, except the values of keys named "kind" (a
	// credential's kind). below: v is a secret key's value, so a value in a mapping under it is
	// a secret only for being there (see nested). Each node is read at most once in each way, so
	// an alias back into its own anchor, or anchors that nest aliases many times over, cost no
	// more than the document's size.
	type place int
	const (
		anywhere  place = iota // a credential: each value is a secret by itself
		below                  // a secret key's value
		inMapping              // in a mapping under a secret key's value
	)
	type allReading struct {
		n    *yaml.Node
		kind bool
		at   place
	}
	allRead := map[allReading]bool{}
	var all func(v *yaml.Node, kind bool, at place)
	all = func(v *yaml.Node, kind bool, at place) {
		k := allReading{v, kind, at}
		if allRead[k] {
			return
		}
		allRead[k] = true
		switch v.Kind {
		case yaml.ScalarNode:
			add(v, at == inMapping)
		case yaml.AliasNode:
			if v.Alias != nil {
				all(v.Alias, kind, at)
			}
		case yaml.SequenceNode:
			for _, c := range v.Content {
				all(c, kind, at)
			}
		case yaml.MappingNode:
			if at == below {
				at = inMapping
			}
			for i := 0; i+1 < len(v.Content); i += 2 {
				if kind || keyName(v.Content[i]) != "kind" {
					all(v.Content[i+1], kind, at)
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
	var keyRule func(key string, val *yaml.Node, c ctx)
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
			all(v, true, below)
			return
		case c.role == roleSections && v.Kind == yaml.MappingNode:
			// Each section is a list of provider entries, read by the rules; any other key is
			// no section core reads, so its values are secrets, as below any secret key.
			pairs(v, c, func(name string, val *yaml.Node) {
				if slices.Contains(sectionNames, name) {
					walk(val, ctx{section: true})
				} else {
					all(val, true, below)
				}
			})
			return
		case c.role == roleEntries && v.Kind == yaml.SequenceNode:
			// An entry is read by the rules; anything else in the list is a key.
			for _, x := range v.Content {
				if resolved(x).Kind != yaml.MappingNode {
					all(x, true, below)
				} else {
					walk(x, ctx{section: true})
				}
			}
			return
		case c.role == roleSections || c.role == roleEntries:
			all(v, true, below)
			return
		case c.role == roleHeaders && v.Kind == yaml.MappingNode:
			pairs(v, c, func(name string, val *yaml.Node) {
				switch {
				case resolved(val).Kind == yaml.MappingNode && secretHeader(name):
					// No header value: a mapping below a name about authentication is below
					// a secret key.
					walk(val, ctx{role: roleSecret, section: c.section})
				case resolved(val).Kind == yaml.MappingNode:
					// No header value: read by the rules for keys (a credential, a secret key).
					keyRule(name, val, plain)
				case secretHeader(name) || secretName(name):
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
					all(val, true, anywhere)
				default:
					walk(val, plain)
				}
			})
			return
		}
		switch v.Kind {
		case yaml.ScalarNode:
			if secretURL(v.Value) {
				add(v, false)
			}
		case yaml.SequenceNode:
			for _, x := range v.Content {
				walk(x, plain)
			}
		case yaml.MappingNode:
			pairs(v, c, func(key string, val *yaml.Node) { keyRule(key, val, c) })
		}
	}
	// keyRule reads the value of a key of a mapping in the place c, by the rules for key names.
	keyRule = func(key string, val *yaml.Node, c ctx) {
		plain := ctx{section: c.section}
		switch {
		case strings.EqualFold(key, "credential"):
			all(val, false, anywhere)
		case strings.EqualFold(key, "headers"):
			walk(val, ctx{role: roleHeaders, section: c.section})
		case headersName(key):
			walk(val, ctx{role: roleHeaders})
		case c.role == roleTop && key == "api-keys" && resolved(val).Kind == yaml.MappingNode:
			walk(val, ctx{role: roleSections})
		case c.role == roleTop && slices.Contains(sectionLists, key):
			walk(val, ctx{role: roleEntries})
		case secretName(key):
			walk(val, ctx{role: roleSecret, section: c.section})
		default:
			walk(val, plain)
		}
	}
	if strict {
		walk(n, ctx{})
	} else {
		walk(n, ctx{role: roleTop})
	}
	// A string secret of MinTextSecret bytes or more that is one by itself (a secret key's
	// value or list item, a credential's value, a secret header's value, a URL), written again
	// as a whole value anywhere else (note: <the key>), is a secret there too: a copy. A shorter
	// or not string copy is not: it carries too little of the secret, and the same 3 or "abc"
	// is often written for other reasons. Nor is a copy of a value that is a secret only for
	// being in a mapping below a secret key (nested): the mode: automatic of a client-secret
	// mapping says nothing secret about routing's strategy: automatic. A placeholder is no
	// secret to copy: in an edited document, one moved elsewhere is no copy (Restore puts a
	// copy back only where the file holds it, and only while the document holds its secret).
	copied := map[string]bool{}
	for _, v := range out {
		if direct[v] && v.Tag == "!!str" && len(v.Value) >= MinTextSecret && !placeholder.MatchString(v.Value) {
			copied[v.Value] = true
		}
	}
	if len(copied) > 0 {
		eachValue(n, func(v *yaml.Node) {
			if v.Tag == "!!str" && copied[v.Value] {
				add(v, false)
			}
		})
	}
	for _, v := range out {
		if !direct[v] {
			if nested == nil {
				nested = map[*yaml.Node]bool{}
			}
			nested[v] = true
		}
	}
	return out, texts, nested
}

// TextSecrets returns the secret values to take out of text where they occur, such as core's
// messages (Redactor.Scrub): every secret of the document (the values Redact hides), except a number shorter than
// MinTextSecret that is a secret only for being in a mapping below a secret key (the 3 of
// client_secret: {rotate_after_days: 3}), which is left in text: taking every 3 out of a message
// would garble it. A string is taken out however short. The redacted document still shows a
// placeholder in the number's place.
func (d *Doc) TextSecrets() []string {
	nodes, _, nested := found(d.root, false)
	var out []string
	for _, n := range nodes {
		number := n.Tag == "!!int" || n.Tag == "!!float"
		if !nested[n] || !number || len(n.Value) >= MinTextSecret {
			out = append(out, n.Value)
		}
	}
	return out
}

// MinTextSecret is the length, in bytes, below which SettingsSecrets leaves a secret out, and
// TextSecrets a number: removing every occurrence of a string that short from a reply or a log
// would mangle unrelated text. A placeholder in a document replaces the whole value, so it
// covers short ones too.
const MinTextSecret = 8

// secretParam reports whether a URL's query parameter is named like a secret: by the rule for
// header and query parameter names (protocol.SecretName: key, token, auth, signature, ...), by
// the rules for key names, or as a signature (sig, sas), a password (pwd, pass, passwd,
// passcode), a signed token or assertion (jwt, client_assertion, credential), or an OAuth code
// or its verifier.
func secretParam(name string) bool {
	switch joinedName(name) {
	case "sig", "sas", "code", "codeverifier", "pwd", "pass", "passwd", "passcode", "jwt", "clientassertion",
		"credential", "credentials":
		return true
	}
	return protocol.SecretName(name) || secretName(name)
}

// SettingsSecrets returns the secret values an account's settings, a JSON object, hold by the
// rules of RedactSettings, to be removed from text where they occur: those of MinTextSecret
// bytes or more. Of a URL, its parts are returned on their own too, as an extension may write
// one without the rest: the forms of its user name and password a proxy URL's are removed in
// (proxyurl.Password; the redaction adds the Basic form), and the value of each query parameter
// named like a secret (secretParam), as written and decoded. Settings that do not parse hold
// none it can find.
func SettingsSecrets(b []byte) []string {
	d, err := Parse(b, false)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if len(s) >= MinTextSecret && !seen[s] {
			seen[s] = true
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
			for _, kv := range strings.Split(u.RawQuery, "&") {
				k, v, _ := strings.Cut(kv, "=")
				if name, err := url.QueryUnescape(k); err != nil || !secretParam(name) {
					continue
				}
				add(v)
				if s, err := url.QueryUnescape(v); err == nil {
					add(s)
				}
				if s, err := url.PathUnescape(v); err == nil {
					add(s)
				}
			}
		}
	}
	return out
}

// Redact returns a config file's contents with a placeholder in place of each secret. The
// rest stays as written, comments included. Where a secret's text cannot be found exactly (a
// block scalar, an anchor before it, a tag with the value on the next line), the document is
// written out again with the placeholders, as Doc.Bytes writes it.
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
	nodes, texts, _ := found(d.root, strict)
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

// eachValue calls f with each scalar node of the tree under n that is a value, not a mapping's
// key, once each (an alias's anchor is where it is written).
func eachValue(n *yaml.Node, f func(*yaml.Node)) {
	switch n.Kind {
	case yaml.ScalarNode:
		f(n)
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			eachValue(n.Content[i], f)
		}
	default:
		for _, c := range n.Content {
			eachValue(c, f)
		}
	}
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
// for in current, the config file as it is now. One that matches no secret in current is an
// ErrStale, wherever it is. Otherwise a placeholder goes back only where its own secret, or a
// copy of it (see found), is in current: at the same keys, and in a list item, in the item of
// current it stands for (slots), so a secret never moves to another account, another provider
// entry or another setting. A copy goes back only while the restored document still holds the
// secret it copies. A document without placeholders comes back as it is.
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
	// Where core reads each placeholder is found with its aliases followed, which is only sound,
	// and costs no more than core's own reading, for a document core's loader reads.
	if err := d.loads(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	// Each placeholder goes back as the secret's text in current, so it keeps its quotes, or
	// in JSON its type. It goes back only where its secret is in current (paths; in a config
	// file, slots), so no one who can edit the file but not read its secrets can put a secret
	// where core would send it somewhere else. Two secrets of current may share a placeholder (a
	// header's 1234 and "1234" are the same header text): a placeholder goes back as the one
	// where it is.
	type secret struct {
		value, tag, text string
		tagged           bool               // written with an explicit tag, which must go back with it
		paths            map[string]*secret // where the placeholder's secrets are, and which is there
	}
	values := map[string]*secret{}
	// current as core reads it: its aliases followed unless core's loader does not read it, in
	// which case each node is read only where it is written.
	cr := curReading{lists: map[string]*yaml.Node{}, marks: map[*yaml.Node]string{}}
	if cur, err := Parse(current, isYAML); err == nil {
		cr.follow = cur.loads() == nil
		where := map[*yaml.Node][]string{}
		readNodes(cur.root, cr.follow, listPlaces, func(n *yaml.Node, at readAt) {
			switch n.Kind {
			case yaml.ScalarNode:
				where[n] = append(where[n], at.at)
			case yaml.SequenceNode:
				cr.lists[at.at] = n
			}
		})
		nodes, texts, _ := found(cur.root, settings)
		for _, n := range nodes {
			p := r.placeholderOf(n, texts)
			cr.marks[n] = p
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
			for _, at := range where[n] {
				if s.paths[at] == nil {
					s.paths[at] = this
				}
			}
		}
	}
	allowed := map[*yaml.Node]bool{}
	for _, n := range secrets(d.root, settings) {
		allowed[n] = true
	}
	// Where core reads each placeholder: in settings, where it is; in a config file, as slots
	// says, where a list item stands for the item of current it is paired with.
	own := paths(d.root)
	reads := map[*yaml.Node][]readAt{}
	if settings {
		for _, n := range held {
			if at, ok := own[n]; ok {
				reads[n] = []readAt{{at: at}}
			}
		}
	} else {
		marks := map[*yaml.Node]string{}
		for _, n := range held {
			marks[n] = n.Value
		}
		reads = slots(d.root, marks, cr)
	}
	repl := map[*yaml.Node]*secret{}
	var copies []copyAt
	for _, n := range held {
		s, ok := values[n.Value]
		// One that stands for no secret is stale wherever it is, so the answer does not
		// depend on which placeholder comes first.
		if !ok {
			return nil, fmt.Errorf("line %d: %w", d.line(n), ErrStale)
		}
		// It goes back only when each place core reads it at is one where its own secret, or a
		// copy of it (see found), is in current, a secret URL too, which is one by its value. One
		// core reads nowhere (a mapping's key) goes back nowhere.
		if len(reads[n]) == 0 {
			return nil, fmt.Errorf("%w: line %d: a redacted value is not where its secret is; write the value itself there", ErrInvalid, d.line(n))
		}
		for _, at := range reads[n] {
			switch {
			case s.paths[at.at] != nil:
			case at.via != 0:
				return nil, fmt.Errorf("%w: line %d: an alias or merge key reads a redacted value where its secret is not; write the value itself there", ErrInvalid, at.via)
			default:
				return nil, fmt.Errorf("%w: line %d: a redacted value is not where its secret is; write the value itself there", ErrInvalid, d.line(n))
			}
		}
		repl[n] = s.paths[reads[n][0].at]
		if !allowed[n] && !(!settings && secretURL(s.value)) {
			copies = append(copies, copyAt{own[n], s.value, d.line(n)})
		}
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
		if out, ok := d.replace(texts); ok {
			if err := restored(out, isYAML, settings, want, copies); err == nil {
				return out, nil
			}
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
	if err := restored(out, isYAML, settings, want, copies); err != nil {
		return nil, err
	}
	return out, nil
}

// copyAt is where Restore put back a copy of a secret (see found): its path, its value and its
// line in the edited document.
type copyAt struct {
	path, value string
	line        int
}

// ErrCopy marks a copy of a secret that the restored document would show. Restore's error
// wraps it with ErrInvalid and the copy's line.
var ErrCopy = errors.New("a redacted value is a copy of a secret the document no longer holds as it was; write the value itself there")

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

// readAt is a place core reads a node at, and the line of the first alias or merge key on the
// way there from the top of the document (0: none; it is read where it is written).
type readAt struct {
	at  string
	via int
}

// loads reports why core's loader, yaml.v3 decoding into any, would not read a config.yaml that
// holds an alias (an anchor that holds itself, too many aliases, ...), or nil. Only a document
// it reads has its aliases followed (readNodes): yaml.v3 refuses one whose aliases would make
// reading it cost too much more than its size.
func (d *Doc) loads() error {
	if !d.YAML || !hasAlias(d.root) {
		return nil
	}
	var v any
	return yaml.Unmarshal(d.src, &v)
}

func hasAlias(n *yaml.Node) bool {
	return n.Kind == yaml.AliasNode || slices.ContainsFunc(n.Content, hasAlias)
}

// isMerge reports whether a mapping's key is a merge key, as yaml.v3 reads one.
func isMerge(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.ShortTag() == "!!merge")
}

// pairsRead calls each with the name and value of each pair of the mapping m as core's loader
// reads them: m's own pairs, then, with follow, the pairs of the mappings merged into m with "<<"
// whose names m does not have, as yaml.v3 merges them: each merged mapping's own pairs before
// those merged into it, and before the next merged mapping's; a name already read is not read
// again. merge is the merge key of m a pair is read through, or nil. Without follow, "<<" is a
// key like any other.
func pairsRead(m *yaml.Node, follow bool, each func(name string, val, merge *yaml.Node)) {
	var seen map[string]bool
	var read func(m, merge *yaml.Node)
	read = func(m, merge *yaml.Node) {
		var key, from *yaml.Node
		for i := 0; i+1 < len(m.Content); i += 2 {
			if follow && isMerge(m.Content[i]) {
				key, from = m.Content[i], m.Content[i+1]
			}
		}
		if key != nil && seen == nil {
			seen = map[string]bool{}
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			k := m.Content[i]
			if follow && isMerge(k) {
				continue
			}
			name := keyName(k)
			if merge != nil && seen[name] {
				continue
			}
			if seen != nil {
				seen[name] = true
			}
			each(name, m.Content[i+1], merge)
		}
		if key == nil {
			return
		}
		if merge == nil {
			merge = key
		}
		sources := []*yaml.Node{from}
		if from.Kind == yaml.SequenceNode {
			sources = from.Content
		}
		for _, x := range sources {
			if x = resolved(x); x.Kind == yaml.MappingNode {
				read(x, merge)
			}
		}
	}
	read(m, nil)
}

// readNodes walks the tree under root as core's loader reads it: with follow, an alias as its
// anchor in the alias's place; a mapping's pairs as pairsRead reads them. It calls f with each
// node and each place it is read at, written as paths writes places, with places giving those of
// the items of a list at a place. Each node is visited once at each of its places, so an anchor
// read through many aliases costs one visit for each place it is read at.
func readNodes(root *yaml.Node, follow bool, places func(list *yaml.Node, at string) []string, f func(n *yaml.Node, at readAt)) {
	type visit struct {
		n  *yaml.Node
		at string
	}
	seen := map[visit]bool{}
	var walk func(n *yaml.Node, at string, via int)
	walk = func(n *yaml.Node, at string, via int) {
		if n.Kind == yaml.AliasNode {
			if !follow || n.Alias == nil {
				return
			}
			if via == 0 {
				via = n.Line
			}
			n = n.Alias
		}
		if seen[visit{n, at}] {
			return
		}
		seen[visit{n, at}] = true
		f(n, readAt{at, via})
		switch n.Kind {
		case yaml.MappingNode:
			pairsRead(n, follow, func(name string, val, merge *yaml.Node) {
				v := via
				if v == 0 && merge != nil {
					v = merge.Line
				}
				walk(val, at+"."+strconv.Quote(name), v)
			})
		case yaml.SequenceNode:
			for i, p := range places(n, at) {
				walk(n.Content[i], p, via)
			}
		}
	}
	walk(root, "", 0)
}

// listPlaces gives the items of a list their places as paths does: a scalar's is [], so it may
// move within its own list, and only there.
func listPlaces(list *yaml.Node, at string) []string {
	out := make([]string, len(list.Content))
	for i, c := range list.Content {
		if resolved(c).Kind == yaml.ScalarNode {
			out[i] = at + "[]"
		} else {
			out[i] = at + "[" + strconv.Itoa(i) + "]"
		}
	}
	return out
}

// curReading is the config file as it is now, as core reads it (readNodes): whether its aliases
// are followed, the list at each place, and the placeholder of each of its secrets.
type curReading struct {
	follow bool
	lists  map[string]*yaml.Node
	marks  map[*yaml.Node]string
}

// slots returns where core reads each placeholder (marks: the placeholders edited holds) of a
// config document sent back, edited, with aliases and merge keys followed (readNodes), in the
// order read, written as the places of cur, the config file as it is now, so that a placeholder
// goes back only where its own secret is in cur. A list item that is not a scalar stands for the
// item of cur's list at the same place it is paired with (pairItems), and for none ([new]) when
// it is paired with none: an account or a provider entry keeps its secrets when it moves within
// its list, and a secret never goes back in another.
func slots(edited *yaml.Node, marks map[*yaml.Node]string, cur curReading) map[*yaml.Node][]readAt {
	out := map[*yaml.Node][]readAt{}
	readNodes(edited, true, func(list *yaml.Node, at string) []string {
		paired := pairItems(list, cur.lists[at], marks, cur)
		places := make([]string, len(list.Content))
		for i, c := range list.Content {
			switch {
			case resolved(c).Kind == yaml.ScalarNode:
				places[i] = at + "[]"
			case paired[i] >= 0:
				places[i] = at + "[" + strconv.Itoa(paired[i]) + "]"
			default:
				places[i] = at + "[new]"
			}
		}
		return places
	}, func(n *yaml.Node, at readAt) {
		if _, ok := marks[n]; ok {
			out[n] = append(out[n], at)
		}
	})
	return out
}

// pairItems pairs each item of edited, a list, that is not a scalar with an item of current, the
// list at the same place in the config file as it is now (cur), or with none (-1); no two with
// the same one. Each item is read as core reads it, an alias as its anchor and a mapping with
// the mappings merged into it. An item with an id, or else a name (an account's id; a provider
// entry's or a registry's name), is paired with the item of current that has the same one, so a
// secret never goes back in an item that has another's id or name. An item whose id or name
// current does not have (a renamed account), or that has neither (a provider entry of a
// config.yaml, a key of its entry), is paired by the secrets it holds: with an item that no
// other is paired with and holds, at the same keys, a secret of each of its placeholders.
func pairItems(edited, current *yaml.Node, marks map[*yaml.Node]string, cur curReading) []int {
	out := make([]int, len(edited.Content))
	for i := range out {
		out[i] = -1
	}
	if current == nil {
		return out
	}
	item := func(n *yaml.Node, follow bool) *yaml.Node {
		if follow {
			return resolved(n)
		}
		return n
	}
	owner := make([]int, len(current.Content)) // the item of edited each is paired with, or -1
	byName := map[[2]string][]int{}
	for j, c := range current.Content {
		owner[j] = -1
		if k, ok := itemName(item(c, cur.follow), cur.follow); ok {
			byName[k] = append(byName[k], j)
		}
	}
	var rest []int
	for i, e := range edited.Content {
		if resolved(e).Kind == yaml.ScalarNode {
			continue
		}
		k, ok := itemName(resolved(e), true)
		js, known := byName[k]
		if !ok || !known {
			rest = append(rest, i)
			continue
		}
		for _, j := range js {
			if owner[j] < 0 {
				owner[j], out[i] = i, j
				break
			}
		}
	}
	// The rest are paired by their secrets: each with an item, not paired by name, that holds all
	// of them; one item may take another's when that one can take a third (a maximum matching),
	// so the order of the items decides nothing.
	type mark struct{ at, placeholder string }
	held := map[mark][]int{} // the items of current, not paired by name, that hold each mark
	has := make([]map[mark]bool, len(current.Content))
	for j, c := range current.Content {
		if owner[j] >= 0 || item(c, cur.follow).Kind == yaml.ScalarNode {
			continue
		}
		has[j] = map[mark]bool{}
		eachMark(c, cur.follow, cur.marks, func(at, p string) {
			m := mark{at, p}
			if !has[j][m] {
				has[j][m] = true
				held[m] = append(held[m], j)
			}
		})
	}
	candidates := map[int][]int{}
	for _, i := range rest {
		var need []mark
		eachMark(edited.Content[i], true, marks, func(at, p string) { need = append(need, mark{at, p}) })
		if len(need) == 0 {
			continue
		}
		for _, j := range held[need[0]] {
			if !slices.ContainsFunc(need, func(m mark) bool { return !has[j][m] }) {
				candidates[i] = append(candidates[i], j)
			}
		}
	}
	seen := make([]int, len(current.Content)) // the search that last looked at each item, plus one
	var take func(i, search int) bool
	take = func(i, search int) bool {
		for _, j := range candidates[i] {
			if seen[j] == search {
				continue
			}
			seen[j] = search
			if owner[j] < 0 || take(owner[j], search) {
				owner[j], out[i] = i, j
				return true
			}
		}
		return false
	}
	for search, i := range rest {
		take(i, search+1)
	}
	return out
}

// itemName returns a list item's id, or else its name, as a key and its value, the mapping read
// as pairsRead reads it.
func itemName(n *yaml.Node, follow bool) ([2]string, bool) {
	if n.Kind != yaml.MappingNode {
		return [2]string{}, false
	}
	var found [2][2]string
	pairsRead(n, follow, func(key string, val, _ *yaml.Node) {
		for i, name := range []string{"id", "name"} {
			if v := resolved(val); key == name && found[i][1] == "" && v.Kind == yaml.ScalarNode && v.Value != "" {
				found[i] = [2]string{key, v.Value}
			}
		}
	})
	for _, k := range found {
		if k[1] != "" {
			return k, true
		}
	}
	return [2]string{}, false
}

// eachMark calls f with the place within n, as readNodes reads it but with every list index left
// out, and the placeholder, of each node of the tree under n that marks has.
func eachMark(n *yaml.Node, follow bool, marks map[*yaml.Node]string, f func(at, placeholder string)) {
	readNodes(n, follow, func(list *yaml.Node, at string) []string {
		out := make([]string, len(list.Content))
		for i := range out {
			out[i] = at + "[]"
		}
		return out
	}, func(n *yaml.Node, at readAt) {
		if p, ok := marks[n]; ok {
			f(at.at, p)
		}
	})
}

// restored returns nil when a document holds no placeholder, holds each wanted secret value
// at least as many times as wanted, and holds a secret of the copy's value at each copy's
// path, so no copy is shown (a copy whose original was changed or deleted is none); else why
// not.
func restored(b []byte, isYAML, strict bool, want map[string]int, copies []copyAt) error {
	failed := fmt.Errorf("%w: could not put the redacted secrets back", ErrInvalid)
	d, err := Parse(b, isYAML)
	if err != nil {
		return failed
	}
	have := map[string]int{}
	where := paths(d.root)
	at := map[[2]string]bool{}
	for _, n := range secrets(d.root, strict) {
		if placeholder.MatchString(n.Value) {
			return failed
		}
		have[n.Value]++
		at[[2]string{where[n], n.Value}] = true
	}
	for _, c := range copies {
		if !at[[2]string{c.path, c.value}] {
			return fmt.Errorf("%w: line %d: %w", ErrInvalid, c.line, ErrCopy)
		}
	}
	for v, n := range want {
		if have[v] < n {
			return failed
		}
	}
	return nil
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
	slices.SortFunc(edits, func(a, b edit) int { return a.start - b.start })
	var out bytes.Buffer
	out.Grow(len(d.src))
	last := 0
	for _, e := range edits {
		if e.start < last { // distinct scalars never overlap; should two, write the document out again
			return nil, false
		}
		out.Write(d.src[last:e.start])
		out.WriteString(e.text)
		last = e.end
	}
	out.Write(d.src[last:])
	return out.Bytes(), true
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
	start := d.offset(n.Line, n.Column)
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
// counts them), or -1. Where each line starts is found once per document, and where each
// character of a line beyond ASCII starts once per line, so many values on one long line cost
// no more than reading it once.
func (d *Doc) offset(line, col int) int {
	if d.lines == nil {
		d.lines = []int{0}
		for i, c := range d.src {
			if c == '\n' {
				d.lines = append(d.lines, i+1)
			}
		}
		d.chars = map[int][]int{}
	}
	if line < 1 || line > len(d.lines) || col < 1 {
		return -1
	}
	start, end := d.lines[line-1], len(d.src)
	if line < len(d.lines) {
		end = d.lines[line] - 1 // its newline
	}
	text := d.src[start:end]
	chars, read := d.chars[line]
	if !read {
		if slices.ContainsFunc(text, func(c byte) bool { return c >= utf8.RuneSelf }) {
			for i := range string(text) {
				chars = append(chars, i)
			}
		}
		d.chars[line] = chars
	}
	switch {
	case chars == nil && col <= len(text):
		return start + col - 1
	case chars != nil && col <= len(chars):
		return start + chars[col-1]
	}
	return -1
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
