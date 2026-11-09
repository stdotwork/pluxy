package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// fail records a wrong key from a and reports whether that blocked it.
func (l *lockout) fail(a netip.Addr) bool {
	_, blockedNow := l.attempt(a, false)
	return blockedNow
}

// blockedFor returns how long a stays blocked, without recording a call.
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
	// While blocked, a right key is refused and changes nothing.
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
	// After the block the count starts again.
	if l.fail(a) {
		t.Error("blocked again by one failure")
	}

	// A right key clears the count.
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

	// A count is forgotten after lockoutIdle without a failure.
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

// Failing from many other addresses does not push a blocked client out of the table.
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

// The management API refuses a client after lockoutFailures wrong keys, even with the right
// key, says when to try again, and still serves other clients.
func TestManagementLockout(t *testing.T) {
	cfg := &Config{ManagementKey: "right-key"}
	m := NewManagement(func() *Config { return cfg }, nil, nil)
	m.Handle("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	call := func(from, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, ManagementPrefix+"/ping", nil)
		r.RemoteAddr = from + ":1234"
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		return w
	}
	for i := range lockoutFailures {
		key := "wrong"
		if i == 0 {
			key = "" // a missing key counts too
		}
		if w := call("198.51.100.7", key); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d answered %d", i+1, w.Code)
		}
	}
	w := call("198.51.100.7", "right-key")
	if w.Code != http.StatusForbidden || w.Header().Get("Retry-After") != "1800" {
		t.Errorf("blocked client with the right key: %d, Retry-After %q; want 403 and 1800", w.Code, w.Header().Get("Retry-After"))
	}
	if w := call("198.51.100.8", "right-key"); w.Code != http.StatusNoContent {
		t.Errorf("another client answered %d", w.Code)
	}
}
