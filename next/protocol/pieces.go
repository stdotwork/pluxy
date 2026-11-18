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
//     user name, password and secret query values (by the same names) of every URL.
//
// A secret made of parts (SecretParts) counts by its parts only, not whole: the words of a
// public prefix, scheme or cookie name ("devin-session-token$", "Bearer", "sid=") are not pieces,
// and neither is a part that is only such words around another part (a header line's
// "Bearer <token>"), nor Basic's decoded "user:password" when its password is a part. A secret
// other than the credential's that is shorter than 8 bytes is left out, as core leaves it in
// text, and so is such a part: a header, proxy or settings secret whose parts are all shorter adds
// nothing. A credential's field whose parts are all shorter adds them, still without the words
// around them. The credential's data is left out too: it mixes secrets with plain values (a base
// URL, a project ID, an OAuth scope) whose words would count as pieces, so an extension passes a
// secret it keeps there itself (sdk.UpstreamText), and core does not remove pieces of it.
func AccountSecrets(a Account) []string {
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
		if short && !slices.ContainsFunc(kept, func(p string) bool { return len(p) >= minSideSecret }) {
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
	if len(a.Settings) > 0 {
		var v any
		if json.Unmarshal(a.Settings, &v) == nil {
			settingsSecrets(v, false, func(s string) { add(s, false) })
		}
	}
	return out
}

// wrapper reports whether the part p of a secret, among its parts, only wraps another part in
// public words: it is split again into a part of any length (a header line's "Bearer <token>"), or
// it is Basic's decoded "user:password" (what a part decodes to from base64) and its password is a
// part too or empty. A part that merely holds another, such as a cookie's value that holds
// another cookie's, is not one. Whether the parts it wraps are kept does not matter: a wrapper is
// never a secret of its own, so a token that has the shape of one ("name:abcd") counts by its
// inner part only, and is left out when that part is too short.
func wrapper(p string, parts []string) bool {
	if len(SecretParts(p, 1)) > 0 {
		return true
	}
	_, pass, ok := strings.Cut(p, ":")
	return ok && (pass == "" || slices.Contains(parts, pass)) && (slices.Contains(parts, base64.StdEncoding.EncodeToString([]byte(p))) ||
		slices.Contains(parts, base64.RawStdEncoding.EncodeToString([]byte(p))))
}

// settingsSecrets calls add with each secret in the settings value v: each string below a key
// whose name is secret (secret says v is), and the secrets of each URL (urlSecrets).
func settingsSecrets(v any, secret bool, add func(string)) {
	switch v := v.(type) {
	case string:
		if secret {
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

// urlSecrets returns the user name and password of the URL s, and the values of its query
// parameters whose names are secret (SecretName) unless proxy is set, as written and
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
		if name, err := url.QueryUnescape(k); err != nil || !SecretName(name) {
			continue
		}
		out = append(out, v)
		if d, err := url.QueryUnescape(v); err == nil && d != v {
			out = append(out, d)
		}
	}
	return out
}

// minPiece is the shortest piece of a secret RemovePieces replaces, as TrimCut counts one.
const minPiece = minCutFragment

// maxDecode bounds how many layers of escapes ReplaceRuns decodes.
const maxDecode = 8

// escapedRun matches a run of token characters and of the escapes that can spell them: JSON's
// \/, \\ and \uXXXX, and percent-encoding's %XX, nested or not.
var escapedRun = regexp.MustCompile(`(?:[A-Za-z0-9_\-.+/=~]|\\u[0-9A-Fa-f]{4}|\\[\\/]|%[0-9A-Fa-f]{2})+`)

// RemovePieces returns s with every run of token characters (letters, digits and "_-.+/=~") that
// holds minPiece or more consecutive characters of one of secrets replaced by Redacted. A run is
// read with its escapes decoded too (ReplaceRuns), so a piece written with escapes between its
// characters is replaced. An upstream that refuses a credential often echoes a piece of it, such
// as its first 24 characters and "...", which a NewRedactor's whole forms and parts miss.
//
// A secret's public prefix (publicKeyPrefix), such as "sk-proj-", gives no piece of its own: a
// piece counts only when it holds at least one byte after the prefix, so words such as "project"
// stay.
//
// Text with words in it, such as a model's reply, would lose every word that shares minPiece
// characters with a secret, so this is for error text: core applies it to an upstream's error
// with the account's secrets (AccountSecrets).
func RemovePieces(s string, secrets ...string) string {
	pieces := map[string]bool{}
	for _, secret := range secrets {
		// A piece starting at i holds a byte after the prefix when i+minPiece > prefix.
		for i := max(0, publicKeyPrefix(secret)-minPiece+1); i+minPiece <= len(secret); i++ {
			pieces[secret[i:i+minPiece]] = true
		}
	}
	if len(pieces) == 0 {
		return s
	}
	return ReplaceRuns(s, func(_, d string) bool {
		for i := 0; i+minPiece <= len(d); i++ {
			if pieces[d[i:i+minPiece]] {
				return true
			}
		}
		return false
	})
}

// maxKeyPrefix is the longest public prefix publicKeyPrefix takes off a secret, and the shortest
// rest of the secret it leaves.
const maxKeyPrefix = 16

// publicKeyPrefix returns the length of the public vendor prefix secret starts with, such as
// "sk-", "sk-proj-", "sk-svcacct-", "sk-ant-api03-", "sk-kimi-" or "xai-": groups of lowercase
// letters and digits, each ending in "-" or "_", the first starting with a letter, at most
// maxKeyPrefix bytes in all (the longest run of whole groups that fits). It is 0 when the
// secret has no such prefix, or when less than maxKeyPrefix bytes follow it: a short secret
// keeps every piece.
func publicKeyPrefix(secret string) int {
	if secret == "" || secret[0] < 'a' || secret[0] > 'z' {
		return 0
	}
	n := 0
	for i := 0; i < len(secret) && i < maxKeyPrefix; i++ {
		c := secret[i]
		switch {
		case c == '-' || c == '_':
			if i == n { // an empty group
				return checkRest(secret, n)
			}
			n = i + 1
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
		default:
			return checkRest(secret, n)
		}
	}
	return checkRest(secret, n)
}

// checkRest returns n, a prefix length of secret, when at least maxKeyPrefix bytes follow it,
// and 0 otherwise.
func checkRest(secret string, n int) int {
	if len(secret)-n < maxKeyPrefix {
		return 0
	}
	return n
}

// ReplaceRuns returns s with every run of token characters and escapes for which match reports
// true replaced by Redacted. match gets the run as written and decoded: JSON's \/, \\ and \uXXXX
// (a surrogate pair as one character) and percent-encoding's %XX, nested up to 8 times.
func ReplaceRuns(s string, match func(run, decoded string) bool) string {
	return escapedRun.ReplaceAllStringFunc(s, func(run string) string {
		d := run
		if strings.ContainsAny(run, `\%`) {
			d = decodeRun(run)
		}
		if match(run, d) {
			return Redacted
		}
		return run
	})
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
