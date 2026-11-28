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
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/private"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/protocol"
)

const PlaceholderPrefix = "pluxy-redacted-"

var placeholder = regexp.MustCompile(`^` + PlaceholderPrefix + `[0-9a-f]{24}$`)

var ErrStale = errors.New("a redacted value matches no secret in the config file as it is now; read the config again")

type Redactor struct{ key []byte }

const KeyIDSize = 32

func NewRedactor() *Redactor {
	key := make([]byte, KeyIDSize)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Redactor{key: key}
}

func LoadKeyID(path string) (*Redactor, error) {
	dir, name := filepath.Dir(path), filepath.Base(path)
	if key, err := readKeyID(dir, name); err == nil {
		return &Redactor{key: key}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	root, err := openKeyRoot(dir, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	key := make([]byte, KeyIDSize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, tmp, err := private.CreateTemp(root, dir, "."+name+".tmp-")
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Remove(tmp)
		}
	}()
	if n, err := f.Write(key); err != nil || n != len(key) {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	err = root.Link(tmp, name)
	switch {
	case err == nil:
		_ = root.Remove(tmp)
		cleanup = false
		if err := private.SyncDir(root); err != nil {
			return nil, err
		}
		return &Redactor{key: key}, nil
	case errors.Is(err, fs.ErrExist):
		existing, rerr := readKeyIDIn(root, dir, name)
		if rerr != nil {
			return nil, rerr
		}
		return &Redactor{key: existing}, nil
	default:
		// Filesystems without hardlink support fall back to atomic rename.
		if rerr := root.Rename(tmp, name); rerr != nil {
			return nil, rerr
		}
		cleanup = false
		if err := private.SyncDir(root); err != nil {
			return nil, err
		}
		return &Redactor{key: key}, nil
	}
}

func openKeyRoot(dir string, create bool) (*os.Root, error) {
	d := private.Dir{Path: dir}
	checked, err := d.Open(create)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := checked.Chmod(private.DirMode); err != nil {
			checked.Close()
			return nil, err
		}
	}
	root, err := d.Root(checked)
	checked.Close()
	return root, err
}

func readKeyID(dir, name string) ([]byte, error) {
	root, err := openKeyRoot(dir, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readKeyIDIn(root, dir, name)
}

func readKeyIDIn(root *os.Root, dir, name string) ([]byte, error) {
	path := filepath.Join(dir, name)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if ok, owner, err := private.Owner(f); err != nil {
		f.Close()
		return nil, err
	} else if !ok {
		f.Close()
		return nil, fmt.Errorf("%s is owned by %s, not by this user", path, owner)
	}
	filePrivate, err := private.FilePrivate(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	key, err := io.ReadAll(io.LimitReader(f, KeyIDSize+1))
	if err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return nil, err
	}
	if len(key) != KeyIDSize {
		return nil, fmt.Errorf("%s: expected %d-byte key, got %d bytes", path, KeyIDSize, len(key))
	}
	if !filePrivate || runtime.GOOS != "windows" && st.Mode().Perm() != private.FileMode {
		if err := private.WriteFile(root, dir, "."+name+".tmp-", name, key); err != nil {
			return nil, fmt.Errorf("making %s private: %w", path, err)
		}
	}
	return key, nil
}

func (r *Redactor) ID(secret string) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(secret))
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

func (r *Redactor) Placeholder(secret string) string { return PlaceholderPrefix + r.ID(secret) }

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

var secretNames = map[string]bool{
	"apikeys": true, "managementkey": true, "clientkeys": true,
}

var secretSuffixes = []string{"token", "secret", "secrets", "password", "passwords", "apikey", "secretkey",
	"privatekey", "secretaccesskey", "passphrase"}

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

func headersName(key string) bool { return strings.Contains(joinedName(key), "header") }

func joinedName(key string) string { return strings.Join(words(key), "") }

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

// folded as encoding/json does when it matches a key to a field: to the smallest letter it is a
// case of (unicode.SimpleFold), so ſ (U+017F) is s and the Kelvin sign is k.
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
	return err == nil && u.Host != "" &&
		(u.User != nil || u.RawQuery != "" || len(protocol.SecretURLValues(u, secretParam)) > 0)
}

var sectionLists, sectionNames = legacy.SectionLists(), legacy.SectionNames()

type ctx struct {
	role    role
	section bool
}

type role int

const (
	rolePlain role = iota
	roleTop
	roleSecret
	roleSections
	roleEntries
	roleHeaders
)

func resolved(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

func keyName(n *yaml.Node) string { return resolved(n).Value }

func secrets(n *yaml.Node, strict bool) []*yaml.Node {
	out, _, _ := found(n, strict)
	return out
}

func found(n *yaml.Node, strict bool) (out []*yaml.Node, texts map[*yaml.Node]string, nested map[*yaml.Node]bool) {
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
	type place int
	const (
		anywhere place = iota
		below
		inMapping
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
	type reading struct {
		n *yaml.Node
		c ctx
	}
	read := map[reading]bool{}
	var walk func(v *yaml.Node, c ctx)
	var keyRule func(key string, val *yaml.Node, c ctx)
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
			pairs(v, c, func(name string, val *yaml.Node) {
				if slices.Contains(sectionNames, name) {
					walk(val, ctx{section: true})
				} else {
					all(val, true, below)
				}
			})
			return
		case c.role == roleEntries && v.Kind == yaml.SequenceNode:
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
					walk(val, ctx{role: roleSecret, section: c.section})
				case resolved(val).Kind == yaml.MappingNode:
					keyRule(name, val, plain)
				case secretHeader(name) || secretName(name):
					if s := resolved(val); c.section && s.Kind == yaml.ScalarNode {
						if t, ok := legacy.HeaderText(s); ok {
							if texts == nil {
								texts = map[*yaml.Node]string{}
							}
							texts[s] = t
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
	keyRule = func(key string, val *yaml.Node, c ctx) {
		plain := ctx{section: c.section}
		switch {
		case strings.EqualFold(key, "credential"):
			all(val, false, anywhere)
		case strings.EqualFold(key, "headers"):
			walk(val, ctx{role: roleHeaders, section: c.section})
		case headersName(key) && !secretName(key):
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

const MinTextSecret = 8

func secretParam(name string) bool {
	switch joinedName(name) {
	case "sig", "sas", "code", "codeverifier", "pwd", "pass", "passwd", "passcode", "jwt", "clientassertion",
		"credential", "credentials":
		return true
	}
	return protocol.SecretName(name) || secretName(name)
}

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
			for _, v := range protocol.SecretURLValues(u, secretParam) {
				add(v)
			}
		}
	}
	return out
}

func (r *Redactor) Redact(b []byte, isYAML bool) ([]byte, error) {
	return r.redact(b, isYAML, false)
}

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

func setScalar(n *yaml.Node, tag, value string, style yaml.Style) {
	*n = yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value, Style: style, Anchor: n.Anchor,
		HeadComment: n.HeadComment, LineComment: n.LineComment, FootComment: n.FootComment}
}

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

func eachScalar(n *yaml.Node, f func(*yaml.Node)) {
	if n.Kind == yaml.ScalarNode {
		f(n)
	}
	for _, c := range n.Content {
		eachScalar(c, f)
	}
}

func (r *Redactor) Restore(edited, current []byte, isYAML bool) ([]byte, error) {
	return r.restore(edited, current, isYAML, false)
}

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
	if err := d.loads(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	type secret struct {
		value, tag, text string
		tagged           bool
		paths            map[string]*secret
	}
	values := map[string]*secret{}
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
	own := paths(d.root)
	reads := map[*yaml.Node][]readAt{}
	var dup *yaml.Node
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
		reads, dup = slots(d.root, marks, cr)
		if k := keyHolding(d.root, marks); k != nil {
			return nil, fmt.Errorf("%w: line %d: a mapping key reads a redacted value; write the value itself there", ErrInvalid, d.line(k))
		}
	}
	repl := map[*yaml.Node]*secret{}
	var copies []copyAt
	for _, n := range held {
		s, ok := values[n.Value]
		if !ok {
			return nil, fmt.Errorf("line %d: %w", d.line(n), ErrStale)
		}
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
	if dup != nil {
		return nil, fmt.Errorf("%w: line %d: an item with a redacted value has its id or name twice (core reads names in any case); keep one, or write the value itself there", ErrInvalid, d.line(dup))
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

type copyAt struct {
	path, value string
	line        int
}

var ErrCopy = errors.New("a redacted value is a copy of a secret the document no longer holds as it was; write the value itself there")

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

type readAt struct {
	at  string
	via int
}

// yaml.v3 decoding into any would not read a config.yaml that holds an alias (an anchor that
// holds itself, too many aliases, ...): yaml.v3 refuses one whose aliases would make reading it
// cost too much more than its size.
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

func isMerge(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.ShortTag() == "!!merge")
}

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

func keyHolding(root *yaml.Node, marks map[*yaml.Node]string) *yaml.Node {
	holds := map[*yaml.Node]bool{}
	var has func(n *yaml.Node) bool
	has = func(n *yaml.Node) bool {
		n = resolved(n)
		if v, ok := holds[n]; ok {
			return v
		}
		holds[n] = false
		_, v := marks[n]
		v = v || slices.ContainsFunc(n.Content, has)
		holds[n] = v
		return v
	}
	var key *yaml.Node
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for i, c := range n.Content {
			if key != nil {
				return
			}
			if n.Kind == yaml.MappingNode && i%2 == 0 && has(c) {
				key = c
				return
			}
			walk(c)
		}
	}
	walk(root)
	return key
}

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

type curReading struct {
	follow bool
	lists  map[string]*yaml.Node
	marks  map[*yaml.Node]string
}

func slots(edited *yaml.Node, marks map[*yaml.Node]string, cur curReading) (out map[*yaml.Node][]readAt, dup *yaml.Node) {
	out = map[*yaml.Node][]readAt{}
	readNodes(edited, true, func(list *yaml.Node, at string) []string {
		paired, d := pairItems(list, cur.lists[at], marks, cur)
		if dup == nil {
			dup = d
		}
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
	return out, dup
}

func pairItems(edited, current *yaml.Node, marks map[*yaml.Node]string, cur curReading) (out []int, dup *yaml.Node) {
	out = make([]int, len(edited.Content))
	for i := range out {
		out[i] = -1
	}
	if current == nil {
		return out, nil
	}
	item := func(n *yaml.Node, follow bool) *yaml.Node {
		if follow {
			return resolved(n)
		}
		return n
	}
	type mark struct{ at, placeholder string }
	owner := make([]int, len(current.Content))
	byName := map[[2]string][]int{}
	held := map[mark][]int{}
	has := make([]map[mark]bool, len(current.Content))
	for j, c := range current.Content {
		owner[j] = -1
		if item(c, cur.follow).Kind == yaml.ScalarNode {
			continue
		}
		if k, ok, _ := itemName(item(c, cur.follow), cur.follow); ok {
			byName[k] = append(byName[k], j)
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
	type group struct {
		need   []mark
		cands  []int
		items  []int
		failed int
	}
	fits := func(need []mark, j int) bool {
		return has[j] != nil && !slices.ContainsFunc(need, func(m mark) bool { return !has[j][m] })
	}
	groups := map[string]*group{}
	var order []*group
	groupOf := map[int]*group{}
	for i, e := range edited.Content {
		if resolved(e).Kind == yaml.ScalarNode {
			continue
		}
		var need []mark
		in := map[mark]bool{}
		eachMark(e, true, marks, func(at, p string) {
			if m := (mark{at, p}); !in[m] {
				in[m] = true
				need = append(need, m)
			}
		})
		if len(need) == 0 {
			continue
		}
		slices.SortFunc(need, func(a, b mark) int { return cmp.Or(cmp.Compare(a.at, b.at), cmp.Compare(a.placeholder, b.placeholder)) })
		var key strings.Builder
		for _, m := range need {
			key.WriteString(strconv.Quote(m.at) + strconv.Quote(m.placeholder))
		}
		g := groups[key.String()]
		if g == nil {
			g = &group{need: need}
			rarest := need[0]
			for _, m := range need[1:] {
				if len(held[m]) < len(held[rarest]) {
					rarest = m
				}
			}
			for _, j := range held[rarest] {
				if fits(need, j) {
					g.cands = append(g.cands, j)
				}
			}
			groups[key.String()] = g
			order = append(order, g)
		}
		g.items = append(g.items, i)
		groupOf[i] = g
		k, ok, d := itemName(resolved(e), true)
		if dup == nil {
			dup = d
		}
		if ok {
			for _, j := range byName[k] {
				if owner[j] < 0 && fits(need, j) {
					owner[j], out[i] = i, j
					break
				}
			}
		}
	}
	slices.SortStableFunc(order, func(a, b *group) int { return len(b.need) - len(a.need) })
	for _, g := range order {
		next := 0
		for _, i := range g.items {
			if out[i] >= 0 {
				continue
			}
			for next < len(g.cands) && owner[g.cands[next]] >= 0 {
				next++
			}
			if next == len(g.cands) {
				break
			}
			owner[g.cands[next]], out[i] = i, g.cands[next]
		}
	}
	seen := make([]int, len(current.Content))
	var take func(i, search int) bool
	take = func(i, search int) bool {
		g := groupOf[i]
		if g.failed == search {
			return false
		}
		for _, j := range g.cands {
			if seen[j] == search {
				continue
			}
			seen[j] = search
			if owner[j] < 0 || take(owner[j], search) {
				owner[j], out[i] = i, j
				return true
			}
		}
		g.failed = search
		return false
	}
	search := 0
search:
	for _, g := range order {
		for _, i := range g.items {
			if out[i] < 0 {
				search++
				if !take(i, search) {
					break search
				}
			}
		}
	}
	for i, e := range edited.Content {
		if k, ok, _ := itemName(resolved(e), true); ok && out[i] < 0 && groupOf[i] != nil {
			if j := slices.IndexFunc(byName[k], func(j int) bool { return owner[j] < 0 }); j >= 0 {
				owner[byName[k][j]], out[i] = i, byName[k][j]
			}
		}
	}
	return out, dup
}

func itemName(n *yaml.Node, follow bool) (k [2]string, ok bool, dup *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		return [2]string{}, false, nil
	}
	var found [2][2]string
	var count [2]int
	pairsRead(n, follow, func(key string, val, _ *yaml.Node) {
		for i, name := range []string{"id", "name"} {
			if fold([]rune(key)) == name {
				if count[i]++; count[i] == 2 && dup == nil {
					dup = val
				}
			}
			if v := resolved(val); key == name && found[i][1] == "" && v.Kind == yaml.ScalarNode && v.Value != "" {
				found[i] = [2]string{key, v.Value}
			}
		}
	})
	for _, k := range found {
		if k[1] != "" {
			return k, true, dup
		}
	}
	return [2]string{}, false, dup
}

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
		if e.start < last {
			return nil, false
		}
		out.Write(d.src[last:e.start])
		out.WriteString(e.text)
		last = e.end
	}
	out.Write(d.src[last:])
	return out.Bytes(), true
}

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

// 1-based line and column (in characters, as yaml.v3 counts them)
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
		end = d.lines[line] - 1
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

func (d *Doc) line(n *yaml.Node) int {
	if d.YAML {
		return n.Line
	}
	return 1 + bytes.Count(d.src[:d.spans[n].start], []byte("\n"))
}

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
