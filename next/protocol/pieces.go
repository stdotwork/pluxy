package protocol

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// minSideSecret is the shortest secret AccountSecrets takes from an account's headers, proxy and
// settings, as core removes a shorter one only next to its name, or not at all: it would match
// unrelated text.
const minSideSecret = 8

// AccountSecrets returns the secrets of account a whose pieces an upstream may echo, for
// RemovePieces. They are the ones an extension sends upstream (sdk.Upstream) or core holds as
// secret:
//
//   - the credential's api_key, access_token and refresh_token;
//   - the value of each header whose name is secret (SecretName);
//   - the user name and password of proxy_url;
//   - in settings, every string below a key whose name is secret (SecretName), and the
//     user name, password and secret query values (secretQueryName) of every URL; a URL below a
//     secret key counts by those parts only, as core's own rule for settings has it.
//
// A secret made of parts (SecretParts) counts by its parts, not whole, unless none of them is
// long enough (below): the words of a public prefix, scheme or cookie name ("devin-session-token$", "Bearer", "sid=") are not pieces,
// and neither is a part that is only such words around another part (a header line's
// "Bearer <token>"), nor Basic's decoded "user:password" when its password is a part. A secret
// other than the credential's that is shorter than 8 bytes is left out, as core leaves it in
// text, and so is such a part; but a header, proxy or settings secret of 8 bytes or more whose
// kept parts are all shorter counts whole, with the words around its parts, so it is never left
// unprotected for being made of short parts (it then also removes a word that shares 4
// characters with its scheme or cookie name). A credential's field whose parts are all shorter
// adds them, still without the words around them. The credential's data is left out too: it
// mixes secrets with plain values (a base URL, a project ID, an OAuth scope) whose words would
// count as pieces, so an extension passes a secret it keeps there itself (sdk.UpstreamText), and
// core does not remove pieces of it.
func AccountSecrets(a Account) []string {
	var settings []string
	if len(a.Settings) > 0 {
		var v any
		if json.Unmarshal(a.Settings, &v) == nil {
			settingsSecrets(v, false, func(s string) { settings = append(settings, s) })
		}
	}
	return accountSecrets(a, settings)
}

// AccountSecretsWith is AccountSecrets with settings in place of the secrets AccountSecrets
// finds in a's settings. Core passes the values its own rules for settings
// (configdoc.SettingsSecrets) find in an account's settings, or in a login's input that is a JSON
// key file, which it passes as settings; and, with an empty account, a login input that is a URL
// (a pasted redirect URL), alone. A value that is an absolute URL counts by its user name,
// password and secret query values only (its other words, such as "https", are no secret), and
// like every secret but the credential's, one under 8 bytes adds nothing.
func AccountSecretsWith(a Account, settings []string) []string {
	var out []string
	for _, s := range settings {
		if absoluteURL(s) {
			out = append(out, urlSecrets(s, false)...)
		} else {
			out = append(out, s)
		}
	}
	return accountSecrets(a, out)
}

// accountSecrets is AccountSecrets with the settings' secrets given.
func accountSecrets(a Account, settings []string) []string {
	var out []string
	add := func(s string, short bool) {
		if s == "" || !short && len(s) < minSideSecret {
			return
		}
		parts := SecretParts(s, 1)
		if len(parts) == 0 {
			parts = []string{s}
		}
		kept := slices.DeleteFunc(slices.Clone(parts), func(p string) bool { return wrapper(p, parts) })
		min := minSideSecret
		if !slices.ContainsFunc(kept, func(p string) bool { return len(p) >= minSideSecret }) {
			if !short {
				// No part reaches the minimum: the whole value counts, not its short parts.
				if !slices.Contains(out, s) {
					out = append(out, s)
				}
				return
			}
			min = 1
		}
		for _, p := range kept {
			if len(p) >= min && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if c := a.Credential; c != nil {
		add(c.APIKey, true)
		add(c.AccessToken, true)
		add(c.RefreshToken, true)
	}
	for name, v := range a.Headers {
		if SecretName(name) {
			add(v, false)
		}
	}
	for _, s := range urlSecrets(a.ProxyURL, true) {
		add(s, false)
	}
	for _, s := range settings {
		add(s, false)
	}
	return out
}

// wrapper reports whether the part p of a secret, among its parts, only wraps another part in
// public words: it is split again into a part of any length (a header line's "Bearer <token>"), or
// it is Basic's decoded "user:password" (what a part decodes to from base64) and its password is a
// part too or empty. A part that merely holds another, such as a cookie's value that holds
// another cookie's, is not one. Whether the parts it wraps are kept does not matter: a wrapper is
// never a secret of its own, so a token that has the shape of one ("name:abcd") counts by its
// inner part only; when no kept part reaches 8 bytes, a header, proxy or settings secret counts
// whole instead (accountSecrets).
func wrapper(p string, parts []string) bool {
	if len(SecretParts(p, 1)) > 0 {
		return true
	}
	_, pass, ok := strings.Cut(p, ":")
	return ok && (pass == "" || slices.Contains(parts, pass)) && (slices.Contains(parts, base64.StdEncoding.EncodeToString([]byte(p))) ||
		slices.Contains(parts, base64.RawStdEncoding.EncodeToString([]byte(p))))
}

// settingsSecrets calls add with each secret in the settings value v: each string below a key
// whose name is secret (secret says v is) but an absolute URL, and the secrets of each URL
// (urlSecrets).
func settingsSecrets(v any, secret bool, add func(string)) {
	switch v := v.(type) {
	case string:
		if secret && !absoluteURL(v) {
			add(v)
		}
		for _, s := range urlSecrets(v, false) {
			add(s)
		}
	case map[string]any:
		for k, x := range v {
			settingsSecrets(x, secret || SecretName(k), add)
		}
	case []any:
		for _, x := range v {
			settingsSecrets(x, secret, add)
		}
	}
}

// absoluteURL reports whether s is an absolute URL with a host.
func absoluteURL(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Host != ""
}

// urlSecrets returns the user name and password of the URL s, and the values of its query
// parameters whose names are secret (secretQueryName) unless proxy is set, as written and
// decoded; nothing for a string that is not an absolute URL.
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
	for _, kv := range strings.Split(u.RawQuery, "&") {
		k, v, _ := strings.Cut(kv, "=")
		if name, err := url.QueryUnescape(k); err != nil || !secretQueryName(name) {
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

// secretQueryName reports whether a URL's query parameter is named like a secret, as core's
// rule for settings (configdoc's secretParam) has it: by SecretName (any name holding key, token,
// secret, password, auth and the like, which covers every name configdoc's key rule takes, such
// as api_key_value or client_secret), or as a signature (sig, sas), a password (pwd, pass,
// passwd, passcode), a signed token or assertion (jwt, client_assertion, credential), an OAuth
// code or its verifier. What it misses is a name written with a letter encoding/json folds to
// another that SecretName's lower-casing does not, such as "ſ" for "s" ("paſſword"), which
// configdoc folds.
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

// minPiece is the shortest piece of a secret RemovePieces replaces, for a secret that is neither
// a long token (longToken) nor a PEM private key. It matches the shortest cut TrimCut removes
// (minCutFragment), but is its own rule: a long token and a PEM key keep longPiece.
const minPiece = minCutFragment

// longSecret is the length, in bytes and with the whitespace around it trimmed (a token read from a
// file can end in a newline), from which a single token counts by longPiece instead of minPiece
// (longToken). A key that long is almost always random, so 4 bytes of it are a coincidence in most
// errors, and would redact an ordinary word such as "test" for a key that spells it; a shorter
// secret, which may be hand-picked, keeps the shorter piece.
const longSecret = 32

// longPiece is the shortest piece RemovePieces takes from a long token and from a PEM private
// key. Both are almost always random, so 4 bytes of one would redact an ordinary word such as
// "Signature" for a key whose body spells "Sign", while a fragment that short of a key is of no
// use. minFragment is the shortest line NewRedactor keeps as
// a secret of its own, so it is the shortest piece of a line here too.
const longPiece = minFragment

// maxDecode bounds how many layers of escapes ReplaceRuns decodes.
const maxDecode = 8

// escapedRun matches a run of token characters and of the escapes that can spell them: JSON's
// \/, \\ and \uXXXX, and percent-encoding's %XX, nested or not.
var escapedRun = regexp.MustCompile(`(?:[A-Za-z0-9_\-.+/=~]|\\u[0-9A-Fa-f]{4}|\\[\\/]|%[0-9A-Fa-f]{2})+`)

// RemovePieces returns s with every run of token characters (letters, digits and "_-.+/=~") that
// holds minPiece or more consecutive characters of one of secrets replaced by Redacted, or
// longPiece or more for a long token (longToken). A run is read as written and with its escapes decoded (ReplaceRuns), so a piece written with escapes
// between its characters is replaced, and so is one of a secret that holds such an escape itself.
// An upstream that refuses a credential often echoes a piece of it, such as its first 24
// characters and "...", which a NewRedactor's whole forms and parts miss.
//
// A secret's public vendor prefix (publicKeyPrefix), such as "sk-proj-", gives no piece of its
// own: a piece counts only when it holds at least one byte after the prefix, so words such as
// "project" stay.
//
// A long token counts at longPiece bytes, each secret by its own length: a part of a longer value
// (AccountSecrets) that is shorter stays at minPiece. Such a key is almost always random, so 4
// bytes of it are a coincidence in an error of any length and would take the error's own words,
// such as "test"; a run of its bytes shorter than longPiece is kept. Any other secret keeps
// minPiece: it may be hand-picked, or hold a shorter secret, and a few bytes of it can be a large
// share of that.
//
// A PEM private key counts by its lines and its unwrapped body, at longPiece bytes too: its body
// is long and random, so a key is redacted whole for a fragment of longPiece bytes or more of
// its body, a shorter run of its body being kept.
//
// Text with words in it, such as a model's reply, would lose every word that shares minPiece
// characters with a secret, so this is for error text: core applies it to an upstream's error
// with the account's secrets (AccountSecrets).
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
			// An upstream that unwraps the key echoes runs across its line breaks.
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
	return ReplaceRuns(s, func(run, d string) bool { return holds(d) || d != run && holds(run) })
}

// longToken reports whether secret, with the whitespace around it trimmed, is one run of token
// characters of longSecret bytes or more that is not base64 (standard or URL-safe, padded or not)
// of text (encodedText). A value with anything else in it (a scheme SecretParts does not split, a
// JSON object, a line break) can hold a shorter secret, and base64 of text is a form of what it
// encodes, such as Basic's user:password, so 4 bytes of either can be a large share of a short
// secret.
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

// encodedText reports whether b, decoded from base64, is text rather than random bytes: valid
// UTF-8, or Basic's user:password with a password in another encoding, which shows as a printable
// ASCII user name before the first colon.
func encodedText(b []byte) bool {
	if utf8.Valid(b) {
		return true
	}
	user, _, ok := strings.Cut(string(b), ":")
	return ok && user != "" && strings.IndexFunc(user, func(r rune) bool { return r < ' ' || r > '~' }) < 0
}

// pieceChar reports whether r is a token character of the runs RemovePieces reads.
func pieceChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.+/=~", r)
}

// isPEMPrivateKey reports whether secret is a PEM private key, the one kind of secret that counts
// by its lines. Any other secret counts as a long token or by minPiece, a line break at its end or
// not: a token read from a file can end in one, and an upstream may echo its first characters.
func isPEMPrivateKey(secret string) bool {
	return strings.Contains(secret, "\n") && strings.Contains(secret, "-----BEGIN ") &&
		strings.Contains(secret, "PRIVATE KEY-----")
}

// holdsPieces reports whether t holds one of set's pieces, each n bytes long.
func holdsPieces(set map[string]bool, t string, n int) bool {
	for i := 0; i+n <= len(t); i++ {
		if set[t[i:i+n]] {
			return true
		}
	}
	return false
}

// addPieces adds to pieces each run of n bytes of s, from the first that holds a byte after s's
// public vendor prefix (publicKeyPrefix).
func addPieces(pieces map[string]bool, s string, n int) {
	// A piece starting at i holds a byte after the prefix when i+n > prefix.
	for i := max(0, publicKeyPrefix(s)-n+1); i+n <= len(s); i++ {
		pieces[s[i:i+n]] = true
	}
}

// publicKeyPrefixes are the public prefixes of vendors' API keys, longest first where one
// starts another, so the longest that fits is taken.
var publicKeyPrefixes = []string{
	"sk-ant-admin01-", "sk-ant-api03-", "sk-ant-oat01-", "sk-ant-ort01-", "sk-svcacct-",
	"sk-admin-", "sk-proj-", "sk-kimi-", "sk-or-v1-", "sk-ant-", "xai-", "gsk_", "sk-",
}

// minAfterPrefix is how many bytes of a secret must follow its public prefix for the prefix to
// count: a shorter secret keeps every piece.
const minAfterPrefix = 16

// publicKeyPrefix returns the length of the public vendor prefix secret starts with (one of
// publicKeyPrefixes, the longest that fits), or 0 when it starts with none of them or less than
// minAfterPrefix bytes follow it.
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

// decodeRun returns run with its escapes decoded, layer by layer, until no escape is left or
// after maxDecode layers.
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

// decodeOnce decodes one layer of the escapes escapedRun matches: \uXXXX (a surrogate pair as
// one character), \/ and \\, and %XX.
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
