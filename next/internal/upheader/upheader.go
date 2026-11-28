// Package upheader reads an account's headers setting (today's headers): extra HTTP headers
// sent on every upstream request of the account, as a map of header name to value. A value of
// "$" and a header name copies that header from the client's request (Fill). The values of
// headers whose names are about authentication (Secret) are secret: never show or log them, and
// never put any value in a message.
package upheader

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/protocol"
)

// refused are the headers a setting may not name: net/http writes them from the request itself,
// so setting them would break or contradict the request.
var refused = []string{"Host", "Content-Length", "Transfer-Encoding", "Connection"}

// notCopied are the client headers a value may not copy: they hold the client's key for pluxy
// (Authorization, X-Api-Key, X-Goog-Api-Key), a proxy's password or cookies, which never go
// upstream.
var notCopied = []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "X-Goog-Api-Key", "Cookie"}

// cpaSession is today's core's own session ID in a value, which this core does not have.
const cpaSession = "$CPA-SESSION-ID"

// copied returns the client header value v copies, and whether it copies one: v starts with "$".
func copied(v string) (name string, ok bool) {
	if !strings.HasPrefix(v, "$") {
		return "", false
	}
	return v[1:], true
}

// Check says what is wrong with headers h, the setting named name in the message; nil when
// nothing is. A name must be an HTTP token other than Host, Content-Length, Transfer-Encoding
// and Connection, named once (letter case aside); a value must not be blank or hold a control
// character other than a tab. A value that starts with "$" copies a client header: the rest must
// be a header name other than those that hold the client's key or cookies (notCopied), and no
// value may hold today's $CPA-SESSION-ID. The message names the header, never its value.
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
		if control(v) {
			return fmt.Errorf("%s: the value of header %s holds a line break or another control character", name, k)
		}
		if strings.Contains(strings.ToUpper(v), cpaSession) {
			return fmt.Errorf("%s: header %s: %s (a CLIProxyAPI session ID, which pluxy does not have) is not supported", name, k, cpaSession)
		}
		if src, ok := copied(v); ok {
			if !token(src) {
				return fmt.Errorf("%s: the value of header %s starts with $, which copies a header from the client's request: it must be $ and a header name, such as $X-Session-Id", name, k)
			}
			if c := http.CanonicalHeaderKey(src); slices.Contains(notCopied, c) {
				return fmt.Errorf("%s: header %s cannot copy the client's %s header: pluxy never sends a client's key, a proxy password or cookies upstream (%s)", name, k, c, strings.Join(notCopied, ", "))
			}
		}
	}
	return nil
}

// Copies reports whether h copies a header from the client's request (Fill).
func Copies(h map[string]string) bool {
	for _, v := range h {
		if _, ok := copied(v); ok {
			return true
		}
	}
	return false
}

// Fill returns headers h, which Check accepts, as a call sends them: each value that copies a
// client header ("$" and its name) becomes that header's first value in client, the name's letter
// case aside, as today's core takes it. A copy is left out when the client did not send the header,
// sent it empty or with a control character other than a tab, so the extension's own value for
// that name, if any, stays; with client nil (traffic that serves no client request, such as a
// token renewal, a login, a model list, a check or a quota read) every copy is left out. It
// returns h itself when h copies nothing, and nil when nothing is left.
func Fill(h map[string]string, client http.Header) map[string]string {
	if !Copies(h) {
		return h
	}
	var out map[string]string
	for k, v := range h {
		if src, ok := copied(v); ok {
			v = clientValue(client, src)
			if strings.TrimSpace(v) == "" || control(v) {
				continue
			}
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// clientValue returns the first value of header name in h, letter case aside; "" for none.
func clientValue(h http.Header, name string) string {
	if v := h.Values(name); len(v) > 0 {
		return v[0]
	}
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// control reports whether v holds a line break or another control character other than a tab,
// which no header value may hold.
func control(v string) bool {
	return strings.ContainsFunc(v, func(r rune) bool { return (r < ' ' && r != '\t') || r == 0x7f })
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
func Secret(name string) bool { return protocol.SecretName(name) }

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
// secret header the value next to its name as text shows a header ("Name: value", "Name:value",
// "Name=value" and JSON's "Name":"value" and "Name": "value", with the name as written, in Go's
// canonical form, in lower case and in upper case), and the value alone when it is at least
// minBare bytes long. In name order. A value shorter than minBare next to its name in another
// spelling stays.
func Forms(h map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(h)) {
		v := h[k]
		if !Secret(k) || v == "" {
			continue
		}
		quoted, _ := json.Marshal(v)
		names := []string{k}
		for _, n := range []string{http.CanonicalHeaderKey(k), strings.ToLower(k), strings.ToUpper(k)} {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		for _, n := range names {
			qn, _ := json.Marshal(n)
			out = append(out, n+": "+v, n+":"+v, n+"="+v, string(qn)+":"+string(quoted), string(qn)+": "+string(quoted))
		}
		if len(v) >= minBare {
			out = append(out, v)
		}
	}
	return out
}

// ImportValue returns a header value as today's core reads it and Import brings it: without the
// spaces around it, and for a value that copies a client header ("$" and its name) without the
// spaces after the "$" too.
func ImportValue(v string) string {
	v = strings.TrimSpace(v)
	if src, ok := copied(v); ok {
		return "$" + strings.TrimSpace(src)
	}
	return v
}

// Left is a header Import leaves out, and why.
type Left struct {
	Name string
	// Why says why, as words that follow "not brought:".
	Why string
}

// Import turns headers as today's core reads them into this core's setting: names and values
// trimmed, and pairs with an empty name or value dropped, as today's core drops them. A value
// that copies a client header ("$" and a header name, the spaces after the "$" trimmed, as
// today's core reads it) is brought as "$" and that name. One whose rest is no header name, which
// today's core never sends, is dropped like an empty one. A copy of a header that holds the
// client's key or cookies (notCopied), and a value with today's $CPA-SESSION-ID, are left out
// and returned in name order. Two names that are the same once trimmed and letter case is ignored
// are refused, naming both, rather than one of them being kept by chance.
func Import(h map[string]string) (out map[string]string, left []Left, err error) {
	seen := map[string]string{} // canonical name -> the name as given
	for _, raw := range slices.Sorted(maps.Keys(h)) {
		k, v := strings.TrimSpace(raw), ImportValue(h[raw])
		if k == "" || v == "" {
			continue
		}
		if strings.Contains(strings.ToUpper(v), cpaSession) {
			left = append(left, Left{k, "its value holds " + cpaSession + ", a CLIProxyAPI session ID that pluxy does not have"})
			continue
		}
		if src, ok := copied(v); ok {
			if !token(src) {
				continue
			}
			if c := http.CanonicalHeaderKey(src); slices.Contains(notCopied, c) {
				left = append(left, Left{k, "it copies the client's " + c + " header, and pluxy never sends a client's key, a proxy password or cookies upstream"})
				continue
			}
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
	return out, left, nil
}
