package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const minSideSecret = 8

// AccountSecrets returns the secrets of account a whose pieces an upstream may echo, for
// RemovePieces. They are the ones an extension sends upstream (sdk.Upstream) or core holds as
// secret:
//
//   - the credential's api_key, access_token and refresh_token;
//   - the value of each header whose name is secret (SecretName);
//   - the user name and password of proxy_url;
//   - in settings, every string or number below a key whose name is secret (SecretName), and the
//     user name, password and secret query or fragment values (secretQueryName) of every URL; a
//     URL below a secret key counts by those parts only.
//
// A secret other than the credential's that is shorter than 8 bytes is left out, and so is such a
// part. A credential's field keeps parts of any length. The credential's data is left out too.
func AccountSecrets(a Account) []string {
	var plain, values []string
	if len(a.Settings) > 0 {
		dec := json.NewDecoder(bytes.NewReader(a.Settings))
		dec.UseNumber()
		var v any
		if json.Valid(a.Settings) && dec.Decode(&v) == nil {
			settingsSecrets(v, false, func(s string) { plain = append(plain, s) }, func(s string) { values = append(values, s) })
		}
	}
	return accountSecrets(a, plain, values)
}

// AccountSecretsWith is AccountSecrets with settings in place of the secrets AccountSecrets
// finds in a's settings. A value that is an absolute URL counts by its user name,
// password and secret query or fragment values only (its other words, such as "https", are no
// secret), and like every secret but the credential's, one under 8 bytes adds nothing.
func AccountSecretsWith(a Account, settings []string) []string {
	var plain, values []string
	for _, s := range settings {
		if absoluteURL(s) {
			values = append(values, urlSecrets(s, false)...)
		} else {
			plain = append(plain, s)
		}
	}
	return accountSecrets(a, plain, values)
}

type fieldKind int

const (
	fieldCredential fieldKind = iota
	fieldSide
	fieldCookie
	fieldURLValue
)

func accountSecrets(a Account, settings, urlValues []string) []string {
	var out []string
	var seen map[string]struct{}
	keep := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		if seen == nil {
			seen = make(map[string]struct{})
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add := func(s string, kind fieldKind) {
		credential := kind == fieldCredential
		if s == "" || !credential && len(s) < minSideSecret {
			return
		}
		c := countedParts(s, kind == fieldCookie)
		min := 1
		if !credential {
			min = minSideSecret
			if !slices.ContainsFunc(c.leaves, func(p string) bool { return len(p) >= minSideSecret }) {
				keep(s)
				return
			}
		}
		if c.past || kind == fieldURLValue {
			keep(s)
		}
		for _, p := range slices.Concat(c.leaves, c.more) {
			if len(p) >= min {
				keep(p)
			}
		}
	}
	if c := a.Credential; c != nil {
		add(c.APIKey, fieldCredential)
		add(c.AccessToken, fieldCredential)
		add(c.RefreshToken, fieldCredential)
	}
	for name, v := range a.Headers {
		if strings.EqualFold(name, "Cookie") {
			add(v, fieldCookie)
		} else if SecretName(name) {
			add(v, fieldSide)
		}
	}
	for _, s := range urlSecrets(a.ProxyURL, true) {
		add(s, fieldURLValue)
	}
	for _, s := range settings {
		add(s, fieldSide)
	}
	for _, s := range urlValues {
		add(s, fieldURLValue)
	}
	return out
}

type partCount struct {
	leaves []string
	more   []string
	past   bool
}

func countedParts(s string, cookie bool) partCount {
	var c partCount
	seen := map[string]bool{}
	var read func(string, int)
	read = func(s string, depth int) {
		if seen[s] {
			return
		}
		seen[s] = true
		shape := directParts(s)
		if len(shape.parts) == 0 {
			c.leaves = append(c.leaves, s)
			return
		}
		if depth == maxPartDepth {
			c.past = true
			c.leaves = append(append(c.leaves, lastWord(s)), shape.leafParts()...)
			return
		}
		if shape.mixed {
			c.more = append(c.more, strings.TrimSpace(s))
		}
		if shape.pair != "" && !cookie {
			c.more = append(c.more, shape.pair)
		}
		for _, p := range shape.parts {
			switch p.kind {
			case partValue:
				c.more = append(c.more, p.text)
				read(p.text, depth+1)
			case partWrapped:
				read(p.text, depth+1)
			case partForm:
				if len(directParts(p.text).parts) > 0 {
					read(p.text, depth+1)
				}
			}
		}
	}
	read(s, 0)
	return c
}

func settingsSecrets(v any, secret bool, add, value func(string)) {
	switch v := v.(type) {
	case string:
		if secret && !absoluteURL(v) {
			add(v)
		}
		for _, s := range urlSecrets(v, false) {
			value(s)
		}
	case json.Number:
		if secret {
			add(v.String())
		}
	case map[string]any:
		for k, x := range v {
			settingsSecrets(x, secret || SecretName(k), add, value)
		}
	case []any:
		for _, x := range v {
			settingsSecrets(x, secret, add, value)
		}
	}
}

func absoluteURL(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Host != ""
}

func urlSecrets(s string, proxy bool) []string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil
	}
	var out []string
	if u.User != nil {
		out = append(out, u.User.Username())
		if p, ok := u.User.Password(); ok {
			out = append(out, p)
		}
	}
	if proxy {
		return out
	}
	return append(out, SecretURLValues(u, secretQueryName)...)
}

// SecretURLValues returns the non-empty values of the parameters of u whose names secret reports,
// as written and decoded. Parameters are read in the query and in the fragment, where an OAuth
// redirect carries them, up to the next "&", and again up to a ";" in them, as some servers split
// on both. A name may follow a page's route in the fragment ("#/callback?code=...").
func SecretURLValues(u *url.URL, secret func(name string) bool) []string {
	var params, out []string
	for _, kv := range slices.Concat(strings.Split(u.RawQuery, "&"), strings.Split(u.EscapedFragment(), "&")) {
		params = append(params, kv)
		if strings.Contains(kv, ";") {
			params = append(params, strings.Split(kv, ";")...)
		}
	}
	named := func(k string) bool {
		name, err := url.QueryUnescape(k)
		return err == nil && secret(name)
	}
	for _, kv := range params {
		k, v, _ := strings.Cut(kv, "=")
		if v == "" || !named(k) && !named(k[strings.LastIndexByte(k, '?')+1:]) {
			continue
		}
		out = append(out, v)
		if d, err := url.QueryUnescape(v); err == nil && d != v {
			out = append(out, d)
		}
		if d, err := url.PathUnescape(v); err == nil && d != v {
			out = append(out, d)
		}
	}
	return out
}

func secretQueryName(name string) bool {
	if SecretName(name) {
		return true
	}
	switch strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(name)) {
	case "sig", "sas", "code", "codeverifier", "pwd", "pass", "passwd", "passcode", "jwt", "clientassertion",
		"credential", "credentials":
		return true
	}
	return false
}

const minPiece = minCutFragment

const longSecret = 32

const longPiece = minFragment

const maxDecode = 8

var escapedRun = regexp.MustCompile(`(?:[A-Za-z0-9_\-.+/=~]|\\u[0-9A-Fa-f]{4}|\\[\\/]|%[0-9A-Fa-f]{2})+`)

// RemovePieces returns s with every run of token characters (letters, digits and "_-.+/=~") that
// holds minPiece or more consecutive characters of one of secrets replaced by Redacted, or
// longPiece or more for a long token (longToken). A run is read as written and with its escapes
// decoded (ReplaceRuns), so a piece written with escapes between its characters is replaced, and so
// is one of a secret that holds such an escape itself.
//
// A run also takes any other character one of secrets holds (secretRunBytes), such as a password's
// "@" or a letter outside ASCII, so a cut of such a secret is one run too. Whitespace, control
// characters, quotes, brackets, commas and backslashes always end a run.
//
// A secret's public vendor prefix (publicKeyPrefix), such as "sk-proj-", gives no piece of its
// own: a piece counts only when it holds at least one byte after the prefix, so words such as
// "project" stay.
//
// A PEM private key counts by its lines and its unwrapped body, at longPiece bytes too.
//
// Text with words in it, such as a model's reply, would lose every word that shares minPiece
// characters with a secret, so this is for error text.
func RemovePieces(s string, secrets ...string) string {
	pieces := map[string]bool{}
	var longPieces map[string]bool
	for _, secret := range secrets {
		if isPEMPrivateKey(secret) {
			if longPieces == nil {
				longPieces = map[string]bool{}
			}
			var body strings.Builder
			for _, line := range strings.Split(secret, "\n") {
				line = strings.TrimSpace(line)
				addPieces(longPieces, line, longPiece)
				if !strings.HasPrefix(line, "-----") {
					body.WriteString(line)
				}
			}
			addPieces(longPieces, body.String(), longPiece)
			continue
		}
		if longToken(secret) {
			if longPieces == nil {
				longPieces = map[string]bool{}
			}
			addPieces(longPieces, secret, longPiece)
			continue
		}
		addPieces(pieces, secret, minPiece)
	}
	if len(pieces) == 0 && len(longPieces) == 0 {
		return s
	}
	holds := func(t string) bool {
		return len(pieces) > 0 && holdsPieces(pieces, t, minPiece) ||
			len(longPieces) > 0 && holdsPieces(longPieces, t, longPiece)
	}
	if extra, ok := secretRunBytes(secrets); ok {
		s = replaceWideRuns(s, &extra, holds)
	}
	return ReplaceRuns(s, func(run, d string) bool { return holds(d) || d != run && holds(run) })
}

func secretRunBytes(secrets []string) (extra [256]bool, ok bool) {
	for _, secret := range secrets {
		for i := range len(secret) {
			c := secret[i]
			if (c >= 0x80 || c > ' ' && c < 0x7f && !pieceChar(rune(c))) && strings.IndexByte("\"'`\\{}[],", c) < 0 {
				extra[c], ok = true, true
			}
		}
	}
	return extra, ok
}

func replaceWideRuns(s string, extra *[256]bool, holds func(string) bool) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if !extra[s[i]] && !pieceChar(rune(s[i])) {
			i++
			continue
		}
		start, wide := i, false
		for i < len(s) && (extra[s[i]] || pieceChar(rune(s[i]))) {
			wide = wide || extra[s[i]]
			i++
		}
		if wide && holds(s[start:i]) {
			b.WriteString(s[last:start])
			b.WriteString(Redacted)
			last = i
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func longToken(secret string) bool {
	s := strings.TrimSpace(secret)
	if len(s) < longSecret || strings.IndexFunc(s, func(r rune) bool { return !pieceChar(r) }) >= 0 {
		return false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && encodedText(b) {
			return false
		}
	}
	return true
}

func encodedText(b []byte) bool {
	if utf8.Valid(b) {
		return true
	}
	user, _, ok := strings.Cut(string(b), ":")
	return ok && user != "" && strings.IndexFunc(user, func(r rune) bool { return r < ' ' || r > '~' }) < 0
}

func pieceChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.+/=~", r)
}

func isPEMPrivateKey(secret string) bool {
	return strings.Contains(secret, "\n") && strings.Contains(secret, "-----BEGIN ") &&
		strings.Contains(secret, "PRIVATE KEY-----")
}

func holdsPieces(set map[string]bool, t string, n int) bool {
	for i := 0; i+n <= len(t); i++ {
		if set[t[i:i+n]] {
			return true
		}
	}
	return false
}

func addPieces(pieces map[string]bool, s string, n int) {
	for i := max(0, publicKeyPrefix(s)-n+1); i+n <= len(s); i++ {
		pieces[s[i:i+n]] = true
	}
}

var publicKeyPrefixes = []string{
	"sk-ant-admin01-", "sk-ant-api03-", "sk-ant-oat01-", "sk-ant-ort01-", "sk-svcacct-",
	"sk-admin-", "sk-proj-", "sk-kimi-", "sk-or-v1-", "sk-ant-", "xai-", "gsk_", "sk-",
}

const minAfterPrefix = 16

func publicKeyPrefix(secret string) int {
	for _, p := range publicKeyPrefixes {
		if strings.HasPrefix(secret, p) {
			if len(secret)-len(p) < minAfterPrefix {
				return 0
			}
			return len(p)
		}
	}
	return 0
}

// ReplaceRuns returns s with every run of token characters and escapes for which match reports
// true replaced by Redacted. match gets the run as written and decoded: JSON's \/, \\ and \uXXXX
// (a surrogate pair as one character) and percent-encoding's %XX, nested up to 8 times. The word
// of a Redacted already in s is never a run, so it never becomes "[[redacted]]".
func ReplaceRuns(s string, match func(run, decoded string) bool) string {
	var b strings.Builder
	last := 0
	for _, m := range escapedRun.FindAllStringIndex(s, -1) {
		run := s[m[0]:m[1]]
		if m[0] > 0 && m[1] < len(s) && s[m[0]-1:m[1]+1] == Redacted {
			continue
		}
		d := run
		if strings.ContainsAny(run, `\%`) {
			d = decodeRun(run)
		}
		if match(run, d) {
			b.WriteString(s[last:m[0]])
			b.WriteString(Redacted)
			last = m[1]
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func decodeRun(run string) string {
	for range maxDecode {
		d := decodeOnce(run)
		if d == run {
			break
		}
		run = d
	}
	return run
}

func decodeOnce(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
		case s[i] == '\\' && i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '\\'):
			b.WriteByte(s[i+1])
			i++
		case s[i] == '\\' && i+5 < len(s) && s[i+1] == 'u':
			r, ok := hex4(s, i+2)
			if !ok {
				b.WriteByte(s[i])
				continue
			}
			i += 5
			if utf16.IsSurrogate(r) && i+6 < len(s) && s[i+1] == '\\' && s[i+2] == 'u' {
				if r2, ok := hex4(s, i+3); ok {
					if p := utf16.DecodeRune(r, r2); p != utf8.RuneError {
						r = p
						i += 6
					}
				}
			}
			b.WriteRune(r)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
