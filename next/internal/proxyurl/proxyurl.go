// Package proxyurl reads core's proxy_url settings (today's proxy-url): the global one and an
// account's. A setting is empty (inherit), "direct" or "none" (no proxy, letter case aside), or
// a proxy URL as protocol.ParseProxyURL accepts it. Its user name and password are secret: show
// a setting only through Redact.
package proxyurl

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/protocol"
)

// direct reports whether s says "no proxy".
func direct(s string) bool {
	s = strings.TrimSpace(s)
	return strings.EqualFold(s, "direct") || strings.EqualFold(s, "none")
}

// Check says what is wrong with setting s, named name in the message; nil when nothing is. The
// message never holds the user name or password.
func Check(name, s string) error {
	s = strings.TrimSpace(s)
	if s == "" || direct(s) {
		return nil
	}
	if _, err := protocol.ParseProxyURL(s); err != nil {
		return fmt.Errorf("%s %q %v (or use direct or none for no proxy)", name, Redact(s), err)
	}
	return nil
}

// Resolve returns the proxy URL a call uses: the account's setting own, else the global one;
// "" for no proxy. Both must have passed Check.
func Resolve(own, global string) string {
	s := strings.TrimSpace(own)
	if s == "" {
		s = strings.TrimSpace(global)
	}
	if direct(s) {
		return ""
	}
	return s
}

// Redact returns setting s fit to show: its scheme, host and port, with "redacted" in place of
// a user name and password. A setting protocol.ParseProxyURL refuses shows as a fixed text, so
// no part of a malformed value's user name or password is quoted.
func Redact(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || direct(s) {
		return s
	}
	u, err := protocol.ParseProxyURL(s)
	if err != nil {
		return "(not a valid proxy URL)"
	}
	r := url.URL{Scheme: u.Scheme, Host: u.Host}
	if u.User != nil {
		r.User = url.User("redacted")
	}
	return r.String()
}

// minBare is the shortest password or user name that is removed from text on its own. A shorter
// one ("1", "pass", "proxy") would also remove it from unrelated text such as model names and
// numbers, so it is removed only as part of the whole user name and password.
const minBare = 8

// Password returns the forms of a proxy URL's secret part that are removed from text, nothing
// when it has no user name or password: its user info as it is written in the URL and decoded
// ("alice:p%40ss", "alice:p@ss"), the "user:password" the proxy login sends (its Basic
// Proxy-Authorization value is the base64 of it), and the password and the user name alone when
// each is at least minBare bytes long.
func Password(s string) []string {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return nil
	}
	user := u.User.Username()
	pass, hasPass := u.User.Password()
	var out []string
	add := func(forms ...string) {
		for _, f := range forms {
			if f != "" && !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	written := writtenUserinfo(s)
	decoded := user
	if hasPass {
		decoded += ":" + pass
	}
	if written == "" && decoded == "" {
		return nil
	}
	add(written, decoded, user+":"+pass)
	if before, after, ok := strings.Cut(written, ":"); ok {
		if len(after) >= minBare {
			add(after)
		}
		if len(before) >= minBare {
			add(before)
		}
	} else if len(written) >= minBare {
		add(written)
	}
	if len(pass) >= minBare {
		add(pass)
	}
	if len(user) >= minBare {
		add(user)
	}
	return out
}

// writtenUserinfo returns the user info of proxy URL s as it is written, before the last "@" of
// its authority; "" when there is none.
func writtenUserinfo(s string) string {
	_, rest, ok := strings.Cut(s, "://")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	i := strings.LastIndex(rest, "@")
	if i < 0 {
		return ""
	}
	return rest[:i]
}
