package sdk

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/protocol"
)

// minSideSecret is the shortest secret AccountSecrets takes from an account's headers, proxy and
// settings, as core removes a shorter one only next to its name, or not at all: it would match
// unrelated text.
const minSideSecret = 8

// AccountSecrets returns the secrets of account a for UpstreamText, so that a piece of any of them
// an upstream echoes is removed. They are the ones sdk.Upstream sends or core holds as secret:
//
//   - the credential's api_key, access_token and refresh_token;
//   - the value of each header whose name is secret (protocol.SecretName);
//   - the user name and password of proxy_url;
//   - in settings, every string below a key whose name is secret (protocol.SecretName), and the
//     user name, password and secret query values (by the same names) of every URL.
//
// A secret made of parts (protocol.SecretParts) counts by its parts only, not whole: the words of a
// public prefix, scheme or cookie name ("devin-session-token$", "Bearer", "sid=") are not pieces,
// and neither is a part that is only such words around another part (a header line's
// "Bearer <token>"), nor Basic's decoded "user:password" when its password is a part. A secret
// other than the credential's that is shorter than 8 bytes is left out, as core leaves it in
// text, and so is such a part: a header, proxy or settings secret whose parts are all shorter adds
// nothing. A credential's field whose parts are all shorter adds them, still without the words
// around them. The credential's data is left out too: it mixes secrets with plain values (a base URL,
// a project ID, an OAuth scope) whose words would count as pieces, so an extension passes a secret
// it keeps there itself.
func AccountSecrets(a protocol.Account) []string {
	var out []string
	add := func(s string, short bool) {
		if s == "" || !short && len(s) < minSideSecret {
			return
		}
		parts := protocol.SecretParts(s, 1)
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
		if protocol.SecretName(name) {
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
	if len(protocol.SecretParts(p, 1)) > 0 {
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
			settingsSecrets(x, secret || protocol.SecretName(k), add)
		}
	case []any:
		for _, x := range v {
			settingsSecrets(x, secret, add)
		}
	}
}

// urlSecrets returns the user name and password of the URL s, and the values of its query
// parameters whose names are secret (protocol.SecretName) unless proxy is set, as written and
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
		if name, err := url.QueryUnescape(k); err != nil || !protocol.SecretName(name) {
			continue
		}
		out = append(out, v)
		if d, err := url.QueryUnescape(v); err == nil && d != v {
			out = append(out, d)
		}
	}
	return out
}
