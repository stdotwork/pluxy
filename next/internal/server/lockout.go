package server

import (
	"net/netip"
	"sync"
	"time"
)

// The management key lockout, with today's core's numbers: a client that sends a missing or
// wrong management key lockoutFailures times in a row is refused for lockoutFor, whatever it
// sends. A client's count is forgotten after lockoutIdle without a failure.
const (
	lockoutFailures = 5
	lockoutFor      = 30 * time.Minute
	lockoutIdle     = 2 * time.Hour
	// lockoutSweep is how often, at most, forgotten clients are removed.
	lockoutSweep = 10 * time.Minute
	// lockoutClients bounds how many clients are tracked; past it the one whose last failure
	// is oldest is dropped.
	lockoutClients = 10000
)

// lockout counts failed management keys per client. An IPv6 client is counted by its /64, the
// block one host is usually given, so it cannot start afresh by changing address within it.
type lockout struct {
	mu      sync.Mutex
	clients map[netip.Prefix]*attempts
	swept   time.Time
	now     func() time.Time
}

type attempts struct {
	failures int
	last     time.Time // of the last failure
	until    time.Time // blocked until then
}

func newLockout() *lockout {
	return &lockout{clients: map[netip.Prefix]*attempts{}, now: time.Now}
}

// lockoutKey is what a client is counted by: its address, or for IPv6 its /64.
func lockoutKey(a netip.Addr) netip.Prefix {
	if !a.IsValid() {
		return netip.Prefix{}
	}
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p
	}
	return netip.PrefixFrom(a, a.BitLen())
}

// blocked returns how long the client stays blocked, or 0.
func (l *lockout) blocked(a netip.Addr) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at := l.clients[lockoutKey(a)]; at != nil {
		if left := at.until.Sub(l.now()); left > 0 {
			return left
		}
	}
	return 0
}

// fail counts a failed key from the client and reports whether that blocked it.
func (l *lockout) fail(a netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	key := lockoutKey(a)
	at := l.clients[key]
	if at == nil || now.Sub(at.last) > lockoutIdle {
		if at == nil && len(l.clients) >= lockoutClients {
			l.dropOldest()
		}
		at = &attempts{}
		l.clients[key] = at
	}
	at.failures++
	at.last = now
	if at.failures < lockoutFailures {
		return false
	}
	at.failures, at.until = 0, now.Add(lockoutFor)
	return true
}

// succeed clears the client's count after a right key.
func (l *lockout) succeed(a netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, lockoutKey(a))
}

// sweep removes clients whose count is forgotten and who are not blocked.
func (l *lockout) sweep(now time.Time) {
	if now.Sub(l.swept) < lockoutSweep {
		return
	}
	l.swept = now
	for k, at := range l.clients {
		if now.Sub(at.last) > lockoutIdle && !now.Before(at.until) {
			delete(l.clients, k)
		}
	}
}

func (l *lockout) dropOldest() {
	var oldest netip.Prefix
	var at *attempts
	for k, a := range l.clients {
		if at == nil || a.last.Before(at.last) {
			oldest, at = k, a
		}
	}
	delete(l.clients, oldest)
}
