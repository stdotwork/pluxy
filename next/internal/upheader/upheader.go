// Package upheader reads an account's headers setting (today's headers): extra HTTP headers
// sent on every upstream request of the account, as a map of header name to value. The values
// of headers whose names are about authentication (Secret) are secret: never show or log them,
// and never put any value in a message.
package upheader

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// refused are the headers a setting may not name: net/http writes them from the request itself,
// so setting them would break or contradict the request.
var refused = []string{"Host", "Content-Length", "Transfer-Encoding", "Connection"}

// Check says what is wrong with headers h, the setting named name in the message; nil when
// nothing is. A name must be an HTTP token other than Host, Content-Length, Transfer-Encoding
// and Connection, named once (letter case aside); a value must not be blank or hold a control
// character other than a tab. The message names the header, never its value.
func Check(name string, h map[string]string) error {
	seen := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(h)) {
		v := h[k]
		if !token(k) {
			return fmt.Errorf("%s: %q is not a valid header name", name, k)
		}
		c := http.CanonicalHeaderKey(k)
		if slices.Contains(refused, c) {
			return fmt.Errorf("%s: header %s cannot be set: %s are set from the request itself", name, k, strings.Join(refused, ", "))
		}
		if other, dup := seen[c]; dup {
			return fmt.Errorf("%s: headers %s and %s are the same header", name, other, k)
		}
		seen[c] = k
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s: header %s has no value", name, k)
		}
		if strings.ContainsFunc(v, func(r rune) bool { return (r < ' ' && r != '\t') || r == 0x7f }) {
			return fmt.Errorf("%s: the value of header %s holds a line break or another control character", name, k)
		}
	}
	return nil
}

// token reports whether s is an HTTP token (RFC 9110 section 5.6.2), as a header name must be.
func token(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// Secret reports whether a header's name is about authentication (Authorization, X-Api-Key,
// Cookie, ...), so its value is a secret. This is the rule config redaction uses for every
// "headers" mapping.
func Secret(name string) bool {
	h := strings.ToLower(name)
	for _, part := range []string{"auth", "key", "token", "secret", "cookie", "session", "password", "signature"} {
		if strings.Contains(h, part) {
			return true
		}
	}
	return false
}

// Secrets returns the values of h that are secret, in name order.
func Secrets(h map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(h)) {
		if Secret(k) && h[k] != "" {
			out = append(out, h[k])
		}
	}
	return out
}

// minBare is the shortest secret value that is removed from text on its own. A shorter one ("1",
// "abc") would also remove it from unrelated text such as model names and numbers, so it is
// removed only next to its name (Forms).
const minBare = 8

// Forms returns the strings to remove from text so that no secret value of h shows: for each
// secret header the value next to its name as text shows a header ("Name: value", "Name=value"
// and JSON's "Name":"value" and "Name": "value", with the name as written, in Go's canonical form and in lower case),
// and the value alone when it is at least minBare bytes long. In name order.
func Forms(h map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(h)) {
		v := h[k]
		if !Secret(k) || v == "" {
			continue
		}
		quoted, _ := json.Marshal(v)
		names := []string{k}
		for _, n := range []string{http.CanonicalHeaderKey(k), strings.ToLower(k)} {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		for _, n := range names {
			qn, _ := json.Marshal(n)
			out = append(out, n+": "+v, n+"="+v, string(qn)+":"+string(quoted), string(qn)+": "+string(quoted))
		}
		if len(v) >= minBare {
			out = append(out, v)
		}
	}
	return out
}

// Import turns headers as today's core reads them into this core's setting: names and values
// trimmed, and pairs with an empty name or value dropped, as today's core drops them. Values that
// today's core fills in from the client's request ("$" and a header name, or $CPA-SESSION-ID)
// are not imported, since this core sends values only as written; their names are returned.
// Two names that are the same once trimmed and letter case is ignored are refused, naming both,
// rather than one of them being kept by chance.
func Import(h map[string]string) (out map[string]string, dynamic []string, err error) {
	seen := map[string]string{} // canonical name -> the name as given
	for _, raw := range slices.Sorted(maps.Keys(h)) {
		k, v := strings.TrimSpace(raw), strings.TrimSpace(h[raw])
		if k == "" || v == "" {
			continue
		}
		if strings.HasPrefix(v, "$") || strings.Contains(strings.ToUpper(v), "$CPA-SESSION-ID") {
			dynamic = append(dynamic, k)
			continue
		}
		c := http.CanonicalHeaderKey(k)
		if other, dup := seen[c]; dup {
			return nil, nil, fmt.Errorf("headers %q and %q are the same header: names that differ only in spaces or letter case", other, raw)
		}
		seen[c] = raw
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	slices.Sort(dynamic)
	return out, dynamic, nil
}
