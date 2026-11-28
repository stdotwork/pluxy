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

var refused = []string{"Host", "Content-Length", "Transfer-Encoding", "Connection"}

var notCopied = []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "X-Goog-Api-Key", "Cookie"}

const cpaSession = "$CPA-SESSION-ID"

func copied(v string) (name string, ok bool) {
	if !strings.HasPrefix(v, "$") {
		return "", false
	}
	return v[1:], true
}

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

func Copies(h map[string]string) bool {
	for _, v := range h {
		if _, ok := copied(v); ok {
			return true
		}
	}
	return false
}

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

func control(v string) bool {
	return strings.ContainsFunc(v, func(r rune) bool { return (r < ' ' && r != '\t') || r == 0x7f })
}

// HTTP token (RFC 9110 section 5.6.2)
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

func Secret(name string) bool { return protocol.SecretName(name) }

func Secrets(h map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(h)) {
		if Secret(k) && h[k] != "" {
			out = append(out, h[k])
		}
	}
	return out
}

const minBare = 8

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

func ImportValue(v string) string {
	v = strings.TrimSpace(v)
	if src, ok := copied(v); ok {
		return "$" + strings.TrimSpace(src)
	}
	return v
}

type Left struct {
	Name string
	Why  string
}

func Import(h map[string]string) (out map[string]string, left []Left, err error) {
	seen := map[string]string{}
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
