package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
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
		{"an empty forwarding header names no one", "127.0.0.1:1", map[string]string{"X-Forwarded-For": " "}, http.StatusNoContent},
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
	// A trusted proxy that names the client only in a way core does not read is not this machine.
	for _, h := range []map[string]string{{"Forwarded": "for=198.51.100.1"}, {"X-Forwarded-For": "unknown"}, {"X-Real-IP": "unknown"}} {
		if w := call("127.0.0.1:1", h); w.Code != http.StatusForbidden {
			t.Errorf("trusted proxy with %v: %d, want 403", h, w.Code)
		}
	}
	if w := call("127.0.0.1:1", nil); w.Code != http.StatusNoContent {
		t.Errorf("the trusted proxy's own call: %d", w.Code)
	}

	// management_allow_remote serves other machines, still with the key.
	cfg = &Config{ManagementKey: "right-key", ManagementAllowRemote: true}
	if w := call("192.0.2.10:1", nil); w.Code != http.StatusNoContent {
		t.Errorf("allow remote: %d", w.Code)
	}
}

// A call from the very address its connection arrived at comes from this machine, as a loopback
// call does: core listening on a network address of its machine still answers the pluxy command
// run there. Another address, a forwarded call and a client a trusted proxy names do not count.
func TestManagementGateOwnAddress(t *testing.T) {
	cfg := &Config{ManagementKey: "right-key"}
	m := NewManagement(func() *Config { return cfg }, nil, nil)
	m.Handle("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	call := func(peer, local, key string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, ManagementPrefix+"/ping", nil)
		r.RemoteAddr = peer
		if local != "" {
			r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.TCPAddrFromAddrPort(netip.MustParseAddrPort(local))))
		}
		r.Header.Set("Authorization", "Bearer "+key)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		WithClientIP(func() *Config { return cfg }, m).ServeHTTP(w, r)
		return w
	}
	cases := []struct {
		name, peer, local string
		headers           map[string]string
		want              int
	}{
		{"IPv4: from the address it arrived at", "192.0.2.1:50000", "192.0.2.1:18417", nil, http.StatusNoContent},
		{"IPv6: from the address it arrived at", "[2001:db8::1]:50000", "[2001:db8::1]:18417", nil, http.StatusNoContent},
		{"IPv6 link-local with its zone", "[fe80::1%eth0]:50000", "[fe80::1%eth0]:18417", nil, http.StatusNoContent},
		{"IPv4-mapped peer, IPv4 listener", "[::ffff:192.0.2.1]:50000", "192.0.2.1:18417", nil, http.StatusNoContent},
		{"IPv4 peer, dual-stack listener", "192.0.2.1:50000", "[::ffff:192.0.2.1]:18417", nil, http.StatusNoContent},
		{"another machine", "192.0.2.10:50000", "192.0.2.1:18417", nil, http.StatusForbidden},
		{"another address of this machine", "198.51.100.7:50000", "192.0.2.1:18417", nil, http.StatusForbidden},
		{"no address it arrived at", "192.0.2.1:50000", "", nil, http.StatusForbidden},
		{"forwarded by a proxy at that address core does not trust", "192.0.2.1:50000", "192.0.2.1:18417", map[string]string{"X-Forwarded-For": "198.51.100.1"}, http.StatusForbidden},
		{"X-Real-IP from that address", "192.0.2.1:50000", "192.0.2.1:18417", map[string]string{"X-Real-IP": "198.51.100.1"}, http.StatusForbidden},
		{"Forwarded from that address", "192.0.2.1:50000", "192.0.2.1:18417", map[string]string{"Forwarded": "for=198.51.100.1"}, http.StatusForbidden},
		{"a remote client naming core's own address", "192.0.2.10:50000", "192.0.2.1:18417", map[string]string{"X-Forwarded-For": "192.0.2.1"}, http.StatusForbidden},
	}
	for _, c := range cases {
		if w := call(c.peer, c.local, "right-key", c.headers); w.Code != c.want {
			t.Errorf("%s: answered %d %s, want %d", c.name, w.Code, w.Body, c.want)
		} else if c.want == http.StatusForbidden && !strings.Contains(w.Body.String(), "remote_disabled") {
			t.Errorf("%s: body %s", c.name, w.Body)
		}
	}

	// A caller at that address is never locked out, as a loopback one is not.
	cfg = &Config{ManagementKey: "right-key", ManagementAllowRemote: true}
	for range lockoutFailures + 1 {
		call("192.0.2.1:50000", "192.0.2.1:18417", "wrong-key", nil)
	}
	if w := call("192.0.2.1:50000", "192.0.2.1:18417", "right-key", nil); w.Code != http.StatusNoContent {
		t.Errorf("right key after %d wrong ones from the address it arrived at: %d %s, want 204", lockoutFailures+1, w.Code, w.Body)
	}

	// Behind a trusted proxy the client it names decides, even when that is core's own address:
	// the TCP connection shows only where the proxy is.
	trusted, _ := parseTrustedProxies([]string{"192.0.2.0/24"})
	cfg = &Config{ManagementKey: "right-key", trusted: trusted}
	if w := call("192.0.2.1:50000", "192.0.2.1:18417", "right-key", map[string]string{"X-Forwarded-For": "198.51.100.1"}); w.Code != http.StatusForbidden {
		t.Errorf("a remote client behind a trusted proxy at core's own address: %d, want 403", w.Code)
	}
	if w := call("192.0.2.9:50000", "192.0.2.1:18417", "right-key", map[string]string{"X-Forwarded-For": "192.0.2.1"}); w.Code != http.StatusForbidden {
		t.Errorf("a trusted proxy naming core's own address: %d, want 403", w.Code)
	}
	if w := call("192.0.2.1:50000", "192.0.2.1:18417", "right-key", nil); w.Code != http.StatusNoContent {
		t.Errorf("a trusted proxy at core's own address calling for itself: %d, want 204", w.Code)
	}
}

// The address a connection arrived at reaches the gate over real connections: plain HTTP, and
// HTTPS with HTTP/2, as core serves its TLS listener. A call from another address of this machine
// to it is refused.
func TestManagementGateOwnAddressOverTheNetwork(t *testing.T) {
	addrs := networkAddresses(t)
	if len(addrs) == 0 {
		// A CI runner always has one; a machine with no network does not.
		if os.Getenv("CI") != "" {
			t.Fatal("no IPv4 address on this machine that is not loopback")
		}
		t.Skip("no IPv4 address on this machine that is not loopback")
	}
	cfg := &Config{ManagementKey: "right-key"}
	m := NewManagement(func() *Config { return cfg }, nil, nil)
	m.Handle("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		w.WriteHeader(http.StatusNoContent)
	})
	var peer atomic.Value // the source address of the last call, as the server saw it
	gate := WithClientIP(func() *Config { return cfg }, m)
	for _, tlsOn := range []bool{false, true} {
		ln, err := net.Listen("tcp", net.JoinHostPort(addrs[0].String(), "0"))
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer.Store(remoteAddr(r))
			gate.ServeHTTP(w, r)
		}))
		srv.Listener.Close()
		srv.Listener = ln
		if tlsOn {
			srv.EnableHTTP2 = true
			srv.StartTLS()
		} else {
			srv.Start()
		}
		// from is a client whose calls leave from source (the zero Addr: the one the system picks).
		from := func(source netip.Addr) *http.Client {
			tr := srv.Client().Transport.(*http.Transport).Clone()
			if tr.TLSClientConfig != nil {
				tr.TLSClientConfig.ServerName = "example.com" // the test certificate names it, not this address
			}
			if source.IsValid() {
				tr.DialContext = (&net.Dialer{LocalAddr: &net.TCPAddr{IP: source.AsSlice()}}).DialContext
			}
			return &http.Client{Transport: tr}
		}
		get := func(client *http.Client) *http.Response {
			t.Helper()
			req, _ := http.NewRequest(http.MethodGet, srv.URL+ManagementPrefix+"/ping", nil)
			req.Header.Set("Authorization", "Bearer right-key")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			client.CloseIdleConnections()
			return resp
		}
		resp := get(from(netip.Addr{}))
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("TLS %v: a call at %s from this machine (source %v) answered %d, want 204", tlsOn, ln.Addr(), peer.Load(), resp.StatusCode)
		}
		if want := map[bool]string{false: "HTTP/1.1", true: "HTTP/2.0"}[tlsOn]; resp.Header.Get("X-Proto") != want {
			t.Errorf("TLS %v: served over %q, want %s", tlsOn, resp.Header.Get("X-Proto"), want)
		}
		// A call from another address of this machine arrives at the first one, so it does not
		// count. NAT on this machine may rewrite such a source to the address called (Docker's
		// masquerade rule does for its bridge's address): that call is skipped, since its source
		// is then the address it arrived at.
		tried := false
		for _, other := range addrs[1:] {
			resp := get(from(other))
			if got, _ := peer.Load().(netip.Addr); got != other {
				t.Logf("TLS %v: a call from %s arrived from %s (rewritten on this machine); not used", tlsOn, other, got)
				continue
			}
			tried = true
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("TLS %v: a call at %s from %s answered %d, want 403", tlsOn, ln.Addr(), other, resp.StatusCode)
			}
			break
		}
		if !tried {
			t.Logf("TLS %v: no other IPv4 address of this machine keeps its source to %s: a call from another address of it is not tried", tlsOn, addrs[0])
		}
		srv.Close()
	}
}

// networkAddresses returns this machine's IPv4 addresses that are not loopback.
func networkAddresses(t *testing.T) []netip.Addr {
	t.Helper()
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []netip.Addr
	for _, a := range ifaces {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok && ip.Unmap().Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}
