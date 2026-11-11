// Package proxyurl reads core's proxy_url settings (today's proxy-url): the global one and an
// account's. A setting is empty (inherit), "direct" or "none" (no proxy, letter case aside), or
// a proxy URL as protocol.ParseProxyURL accepts it. Its user name and password are secret: show
// a setting only through Redact.
package proxyurl

import (
	"fmt"
	"net/url"
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
// a user name and password. A setting that does not parse shows as a fixed text.
func Redact(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || direct(s) {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "(not a valid proxy URL)"
	}
	r := url.URL{Scheme: u.Scheme, Host: u.Host}
	if u.User != nil {
		r.User = url.User("redacted")
	}
	return r.String()
}

// Password returns the secret part of a proxy URL: its user name and password as they are
// written, then the password alone; nothing when it has none.
func Password(s string) []string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.User == nil {
		return nil
	}
	var out []string
	if p, ok := u.User.Password(); ok && p != "" {
		out = append(out, u.User.Username()+":"+p, p)
	} else if n := u.User.Username(); n != "" {
		out = append(out, n)
	}
	return out
}
