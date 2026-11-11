package proxyurl

import (
	"slices"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	for _, ok := range []string{"", "  ", "direct", "NONE", " Direct ", "http://proxy:3128", "https://u:p@proxy", "socks5://proxy:1080/", "socks5h://u@proxy:1080"} {
		if err := Check("proxy_url", ok); err != nil {
			t.Errorf("Check(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"proxy:3128", "ftp://proxy:21", "http://", "http://proxy:0", "http://proxy:70000", "http://proxy/path", "http://proxy?x=1", "http://proxy#f", "mailto:a@b"} {
		err := Check("proxy_url", bad)
		if err == nil || !strings.HasPrefix(err.Error(), "proxy_url ") {
			t.Errorf("Check(%q) = %v, want an error naming proxy_url", bad, err)
		}
	}
}

// A value protocol.ParseProxyURL refuses never has any part of its user info in the message, or
// in what Redact shows; a value it accepts shows its scheme and host.
func TestRedactAndCheckNeverQuoteUserinfo(t *testing.T) {
	for _, c := range []struct {
		in      string
		secrets []string
	}{
		{"socks5://alice:2024?x@proxy.example:1080", []string{"alice", "2024"}},
		{"http://alice:1234/5678@proxy:3128", []string{"alice", "1234", "5678"}},
		{"http://alice:99#z@proxy:3128", []string{"alice", "99"}},
		{"ftp://alice:hunter2@proxy:21", []string{"alice", "hunter2"}},
		{"http://alice:hunter2@proxy:99999", []string{"alice", "hunter2"}},
		{"http://alice:hunter2@", []string{"alice", "hunter2"}},
		{"alice:hunter2@proxy:3128", []string{"alice", "hunter2"}},
	} {
		shown := Redact(c.in)
		err := Check("proxy_url", c.in)
		if err == nil {
			t.Fatalf("Check(%q) = nil, want an error", c.in)
		}
		for _, secret := range c.secrets {
			if strings.Contains(shown, secret) || strings.Contains(err.Error(), secret) {
				t.Errorf("%q shows %q in Redact %q or Check %q", c.in, secret, shown, err)
			}
		}
		if shown != "(not a valid proxy URL)" {
			t.Errorf("Redact(%q) = %q, want the fixed text", c.in, shown)
		}
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "",
		" Direct ":                            "Direct",
		"none":                                "none",
		"http://proxy:3128":                   "http://proxy:3128",
		"http://alice:s3cret@proxy:3128":      "http://redacted@proxy:3128",
		"socks5h://alice@proxy.example:1080/": "socks5h://redacted@proxy.example:1080",
		"https://alice:@[::1]:8443":           "https://redacted@[::1]:8443",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	for _, c := range []struct{ own, global, want string }{
		{"", "", ""},
		{"", "http://g:1", "http://g:1"},
		{"http://own:1", "http://g:1", "http://own:1"},
		{" http://own:1 ", "", "http://own:1"},
		{"direct", "http://g:1", ""},
		{"NONE", "http://g:1", ""},
		{"", "direct", ""},
		{"", " none ", ""},
	} {
		if got := Resolve(c.own, c.global); got != c.want {
			t.Errorf("Resolve(%q, %q) = %q, want %q", c.own, c.global, got, c.want)
		}
	}
}

// Password returns the whole user info as written and decoded and the Basic login's text always,
// and the password and user name alone only from 8 bytes.
func TestPassword(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    []string // forms that must be returned
		absent  []string // forms that must not be returned
		nothing bool
	}{
		{in: "http://proxy:3128", nothing: true},
		{in: "http://@proxy:3128", nothing: true},
		{in: "direct", nothing: true},
		{in: "http://alice:pass@proxy:3128", want: []string{"alice:pass"}, absent: []string{"pass", "alice"}},
		{in: "http://u:1@proxy:3128", want: []string{"u:1"}, absent: []string{"1", "u"}},
		{in: "http://alice:p%40ss@proxy:3128", want: []string{"alice:p%40ss", "alice:p@ss"}, absent: []string{"p@ss", "p%40ss"}},
		{in: "http://alice:Long-Secret-1@proxy:3128", want: []string{"alice:Long-Secret-1", "Long-Secret-1"}, absent: []string{"alice"}},
		{in: "http://alice:p%40ss-Long-Secret@proxy:3128", want: []string{"alice:p%40ss-Long-Secret", "alice:p@ss-Long-Secret", "p%40ss-Long-Secret", "p@ss-Long-Secret"}},
		{in: "http://operator-account:x@proxy:3128", want: []string{"operator-account:x", "operator-account"}, absent: []string{"x"}},
		{in: "http://alice@proxy:3128", want: []string{"alice", "alice:"}},
		{in: "http://alice:@proxy:3128", want: []string{"alice:"}},
		{in: "http://:pw@proxy:3128", want: []string{":pw"}},
	} {
		got := Password(c.in)
		if c.nothing {
			if len(got) != 0 {
				t.Errorf("Password(%q) = %q, want none", c.in, got)
			}
			continue
		}
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("Password(%q) = %q, missing %q", c.in, got, w)
			}
		}
		for _, a := range c.absent {
			if slices.Contains(got, a) {
				t.Errorf("Password(%q) = %q, holds %q on its own", c.in, got, a)
			}
		}
	}
}
