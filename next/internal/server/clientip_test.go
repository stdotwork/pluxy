package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestTrustedProxiesConfig(t *testing.T) {
	got, err := parseTrustedProxies([]string{"10.0.0.0/8", " 127.0.0.1 ", "::1", "fd00::/8", "::ffff:192.168.0.0/112"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "127.0.0.1/32", "::1/128", "fd00::/8", "192.168.0.0/16"}
	for i, p := range got {
		if p.String() != want[i] {
			t.Errorf("entry %d = %s, want %s", i, p, want[i])
		}
	}
	for _, bad := range []string{"not-an-ip", "10.0.0.0/33", "fe80::1%eth0", ""} {
		if _, err := parseTrustedProxies([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := ParseConfig([]byte(`{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","trusted_proxies":["nope"]}`), "config.json"); err == nil || !strings.Contains(err.Error(), "trusted_proxies[0]") {
		t.Errorf("ParseConfig with a bad trusted proxy = %v", err)
	}
}

func TestClientIP(t *testing.T) {
	trusted, err := parseTrustedProxies([]string{"10.0.0.0/8", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, peer string
		headers    map[string][]string
		want       string
	}{
		{"no proxy", "203.0.113.5:4000", nil, "203.0.113.5"},
		{"untrusted peer's header is ignored", "203.0.113.5:4000", map[string][]string{"X-Forwarded-For": {"198.51.100.1"}}, "203.0.113.5"},
		{"trusted peer without a header", "10.1.2.3:4000", nil, "10.1.2.3"},
		{"trusted peer names the client", "10.1.2.3:4000", map[string][]string{"X-Forwarded-For": {"198.51.100.1"}}, "198.51.100.1"},
		{"a spoofed entry left of the real client is skipped", "10.1.2.3:4000", map[string][]string{"X-Forwarded-For": {"1.1.1.1, 198.51.100.1, 10.9.9.9"}}, "198.51.100.1"},
		{"every line of the header counts", "127.0.0.1:4000", map[string][]string{"X-Forwarded-For": {"1.1.1.1", "198.51.100.1"}}, "198.51.100.1"},
		{"all trusted: the first", "10.1.2.3:4000", map[string][]string{"X-Forwarded-For": {"10.0.0.7, 10.0.0.8"}}, "10.0.0.7"},
		{"an entry that is not an address spoils the header", "10.1.2.3:4000", map[string][]string{"X-Forwarded-For": {"198.51.100.1, unknown"}}, "10.1.2.3"},
		{"X-Real-IP after a spoiled X-Forwarded-For", "10.1.2.3:4000", map[string][]string{"X-Forwarded-For": {"bad"}, "X-Real-IP": {"198.51.100.2"}}, "198.51.100.2"},
		{"X-Real-IP alone", "10.1.2.3:4000", map[string][]string{"X-Real-IP": {"198.51.100.2"}}, "198.51.100.2"},
		{"IPv4 in IPv6 peer", "[::ffff:10.1.2.3]:4000", map[string][]string{"X-Forwarded-For": {"::ffff:198.51.100.1"}}, "198.51.100.1"},
		{"IPv6 client", "10.1.2.3:4000", map[string][]string{"X-Forwarded-For": {"2001:db8::1"}}, "2001:db8::1"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = c.peer
		for k, vs := range c.headers {
			for _, v := range vs {
				r.Header.Add(k, v)
			}
		}
		if got := clientIP(trusted, r); got != netip.MustParseAddr(c.want) {
			t.Errorf("%s: client %s, want %s", c.name, got, c.want)
		}
	}
	// Without trusted proxies no header counts.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := clientIP(nil, r); got != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("no trusted proxies: client %s", got)
	}
}

// WithClientIP uses the trusted proxies of the config active when the request arrives.
func TestWithClientIPFollowsTheActiveConfig(t *testing.T) {
	cfg := &Config{}
	var seen netip.Addr
	h := WithClientIP(func() *Config { return cfg }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = ClientIP(r) }))
	req := func() {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.1:1"
		r.Header.Set("X-Forwarded-For", "198.51.100.1")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	req()
	if seen.String() != "10.0.0.1" {
		t.Errorf("before: client %s, want the peer", seen)
	}
	trusted, _ := parseTrustedProxies([]string{"10.0.0.0/8"})
	cfg = &Config{trusted: trusted}
	req()
	if seen.String() != "198.51.100.1" {
		t.Errorf("after: client %s, want the forwarded client", seen)
	}
}
