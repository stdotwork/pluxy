package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The management API answers only this machine unless management_allow_remote is on; a request
// a proxy core does not trust forwards from elsewhere does not count as this machine.
func TestManagementRemoteGate(t *testing.T) {
	cfg := &Config{ManagementKey: "right-key"}
	m := NewManagement(func() *Config { return cfg }, nil, nil)
	m.Handle("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	call := func(peer string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, ManagementPrefix+"/ping", nil)
		r.RemoteAddr = peer
		r.Header.Set("Authorization", "Bearer right-key")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		WithClientIP(func() *Config { return cfg }, m).ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct {
		name, peer string
		headers    map[string]string
		want       int
	}{
		{"IPv4 loopback", "127.0.0.1:1", nil, http.StatusNoContent},
		{"another loopback address", "127.0.0.2:1", nil, http.StatusNoContent},
		{"IPv6 loopback", "[::1]:1", nil, http.StatusNoContent},
		{"IPv4 loopback as IPv6", "[::ffff:127.0.0.1]:1", nil, http.StatusNoContent},
		{"another machine", "192.0.2.10:1", nil, http.StatusForbidden},
		{"forwarded by a local proxy core does not trust", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.1"}, http.StatusForbidden},
		{"X-Real-IP from a local proxy core does not trust", "127.0.0.1:1", map[string]string{"X-Real-IP": "198.51.100.1"}, http.StatusForbidden},
		{"Forwarded from a local proxy core does not trust", "127.0.0.1:1", map[string]string{"Forwarded": "for=198.51.100.1"}, http.StatusForbidden},
		{"a remote client claiming to be local", "192.0.2.10:1", map[string]string{"X-Forwarded-For": "127.0.0.1"}, http.StatusForbidden},
	} {
		if w := call(c.peer, c.headers); w.Code != c.want {
			t.Errorf("%s: answered %d %s, want %d", c.name, w.Code, w.Body, c.want)
		} else if c.want == http.StatusForbidden && !strings.Contains(w.Body.String(), "remote_disabled") {
			t.Errorf("%s: body %s", c.name, w.Body)
		}
	}

	// Behind a trusted proxy the forwarded client decides.
	trusted, _ := parseTrustedProxies([]string{"127.0.0.1"})
	cfg = &Config{ManagementKey: "right-key", trusted: trusted}
	if w := call("127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.1"}); w.Code != http.StatusForbidden {
		t.Errorf("remote client behind a trusted proxy: %d", w.Code)
	}
	if w := call("127.0.0.1:1", map[string]string{"X-Forwarded-For": "::1"}); w.Code != http.StatusNoContent {
		t.Errorf("local client behind a trusted proxy: %d", w.Code)
	}

	// management_allow_remote serves other machines, still with the key.
	cfg = &Config{ManagementKey: "right-key", ManagementAllowRemote: true}
	if w := call("192.0.2.10:1", nil); w.Code != http.StatusNoContent {
		t.Errorf("allow remote: %d", w.Code)
	}
}
