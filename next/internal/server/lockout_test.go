package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func (l *lockout) fail(a netip.Addr) bool {
	_, blockedNow := l.attempt(a, false)
	return blockedNow
}

func (l *lockout) blockedFor(a netip.Addr) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at := l.clients[lockoutKey(a)]; at != nil && at.until.After(l.now()) {
		return at.until.Sub(l.now())
	}
	return 0
}

func TestLockout(t *testing.T) {
	clock := &fakeClock{time.Unix(1_800_000_000, 0)}
	l := newLockout()
	l.now = clock.now
	a := netip.MustParseAddr("198.51.100.7")
	for i := 1; i < lockoutFailures; i++ {
		if l.fail(a) || l.blockedFor(a) != 0 {
			t.Fatalf("blocked after %d failures", i)
		}
	}
	if !l.fail(a) || l.blockedFor(a) != lockoutFor {
		t.Fatalf("not blocked for %s after %d failures: %s", lockoutFor, lockoutFailures, l.blockedFor(a))
	}
	clock.t = clock.t.Add(time.Minute)
	if left, _ := l.attempt(a, true); left != lockoutFor-time.Minute || l.blockedFor(a) != left {
		t.Errorf("right key while blocked: left %s, still blocked %s", left, l.blockedFor(a))
	}
	if other := netip.MustParseAddr("198.51.100.8"); l.blockedFor(other) != 0 {
		t.Error("another address is blocked")
	}
	clock.t = clock.t.Add(lockoutFor)
	if left, _ := l.attempt(a, true); left != 0 {
		t.Error("still blocked after the block ended")
	}
	if l.fail(a) {
		t.Error("blocked again by one failure")
	}

	b := netip.MustParseAddr("203.0.113.1")
	for range lockoutFailures - 1 {
		l.fail(b)
	}
	if left, _ := l.attempt(b, true); left != 0 {
		t.Fatal("right key refused")
	}
	if l.fail(b) {
		t.Error("count not cleared by a right key")
	}

	c := netip.MustParseAddr("203.0.113.2")
	for range lockoutFailures - 1 {
		l.fail(c)
	}
	clock.t = clock.t.Add(lockoutIdle + time.Second)
	if l.fail(c) {
		t.Error("an old count still counted")
	}
}

func TestLockoutCountsIPv6By64(t *testing.T) {
	l := newLockout()
	for i := range lockoutFailures {
		l.fail(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 15: byte(i + 1)}))
	}
	if l.blockedFor(netip.MustParseAddr("2001:db8::ffff")) == 0 {
		t.Error("an address in the same /64 is not blocked")
	}
	if l.blockedFor(netip.MustParseAddr("2001:db8:0:1::1")) != 0 {
		t.Error("an address in another /64 is blocked")
	}
}

func TestLockoutBounded(t *testing.T) {
	l := newLockout()
	l.max = 100
	for i := range 3 * l.max {
		l.fail(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	if n := len(l.clients); n != l.max {
		t.Errorf("%d clients tracked, want %d", n, l.max)
	}
}

func TestLockoutKeepsBlockedClientsWhenFull(t *testing.T) {
	l := newLockout()
	l.max = 100
	a := netip.MustParseAddr("198.51.100.7")
	for range lockoutFailures {
		l.fail(a)
	}
	for i := range 3 * l.max {
		l.fail(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	if l.blockedFor(a) == 0 {
		t.Error("the blocked client was dropped to make room")
	}
}

func lockoutCall(m *Management, from, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, ManagementPrefix+"/ping", nil)
	r.RemoteAddr = net.JoinHostPort(from, "1234")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}

func lockoutManagement() *Management {
	cfg := &Config{ManagementKey: "right-key", ManagementAllowRemote: true}
	m := NewManagement(func() *Config { return cfg }, nil, nil)
	m.Handle("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return m
}

func TestManagementLockout(t *testing.T) {
	m := lockoutManagement()
	for i := range lockoutFailures {
		if w := lockoutCall(m, "198.51.100.7", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d answered %d", i+1, w.Code)
		}
	}
	w := lockoutCall(m, "198.51.100.7", "right-key")
	if w.Code != http.StatusForbidden || w.Header().Get("Retry-After") != "1800" || !strings.Contains(w.Body.String(), `"blocked"`) {
		t.Errorf("blocked client with the right key: %d %s, Retry-After %q; want 403 blocked and 1800", w.Code, w.Body, w.Header().Get("Retry-After"))
	}
	if w := lockoutCall(m, "198.51.100.8", "right-key"); w.Code != http.StatusNoContent {
		t.Errorf("another client answered %d", w.Code)
	}
}

func TestManagementLockoutIgnoresMissingKey(t *testing.T) {
	m := lockoutManagement()
	for i := range lockoutFailures + 1 {
		if w := lockoutCall(m, "198.51.100.7", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("keyless call %d answered %d, want 401", i+1, w.Code)
		}
	}
	if w := lockoutCall(m, "198.51.100.7", "right-key"); w.Code != http.StatusNoContent {
		t.Errorf("after %d keyless calls, the right key answered %d %s, want 204", lockoutFailures+1, w.Code, w.Body)
	}
}

func TestManagementLockoutSparesThisMachine(t *testing.T) {
	m := lockoutManagement()
	for _, from := range []string{"127.0.0.1", "::1"} {
		for range lockoutFailures + 1 {
			if w := lockoutCall(m, from, "wrong"); w.Code != http.StatusUnauthorized {
				t.Fatalf("wrong key from %s answered %d, want 401", from, w.Code)
			}
		}
		if w := lockoutCall(m, from, "right-key"); w.Code != http.StatusNoContent {
			t.Errorf("%s after %d wrong keys, with the right key: %d %s, want 204", from, lockoutFailures+1, w.Code, w.Body)
		}
	}
	for range lockoutFailures + 1 {
		lockoutCall(m, "198.51.100.9", "wrong")
	}
	if w := lockoutCall(m, "198.51.100.9", "right-key"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"blocked"`) {
		t.Errorf("another machine after %d wrong keys, with the right key: %d %s, want 403 blocked", lockoutFailures+1, w.Code, w.Body)
	}
}
