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
	// lockoutClients bounds how many clients are tracked; past it one is dropped (dropOne).
	lockoutClients = 10000
)

// lockout counts failed management keys per client. An IPv6 client is counted by its /64, the
// block one host is usually given, so it cannot start afresh by changing address within it.
type lockout struct {
	mu      sync.Mutex
	clients map[netip.Prefix]*attempts
	max     int // lockoutClients; smaller in tests
	swept   time.Time
	now     func() time.Time
}

type attempts struct {
	failures int
	last     time.Time // of the last failure
	until    time.Time // blocked until then
}

func newLockout() *lockout {
	return &lockout{clients: map[netip.Prefix]*attempts{}, max: lockoutClients, now: time.Now}
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

// attempt records one call from the client, whose key was right or not, in one step: it returns
// how long the client stays blocked (0 when it is not, and then the call may go on when the key
// was right), and whether this call's failure blocked it. A blocked client's calls change
// nothing; a right key clears the count; a wrong one adds to it.
func (l *lockout) attempt(a netip.Addr, right bool) (left time.Duration, blockedNow bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := lockoutKey(a)
	at := l.clients[key]
	if at != nil {
		if left := at.until.Sub(now); left > 0 {
			return left, false
		}
	}
	if right {
		delete(l.clients, key)
		return 0, false
	}
	l.sweep(now)
	if at == nil || now.Sub(at.last) > lockoutIdle {
		if at == nil && len(l.clients) >= l.max {
			l.dropOne(now)
		}
		at = &attempts{}
		l.clients[key] = at
	}
	at.failures++
	at.last = now
	if at.failures < lockoutFailures {
		return 0, false
	}
	at.failures, at.until = 0, now.Add(lockoutFor)
	return 0, true
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

// dropOne makes room for a new client: it drops the client that is not blocked whose last
// failure is oldest or, when every client is blocked, the one whose block ends first. So a
// client cannot end its own block by failing from many other addresses until it is dropped:
// that needs every tracked client blocked, which takes more failed keys from more addresses than
// those addresses could try fresh.
func (l *lockout) dropOne(now time.Time) {
	var victim netip.Prefix
	var vat *attempts
	better := func(a *attempts) bool {
		aBlocked, vBlocked := now.Before(a.until), now.Before(vat.until)
		if aBlocked != vBlocked {
			return !aBlocked
		}
		if aBlocked {
			return a.until.Before(vat.until)
		}
		return a.last.Before(vat.last)
	}
	for k, a := range l.clients {
		if vat == nil || better(a) {
			victim, vat = k, a
		}
	}
	delete(l.clients, victim)
}
