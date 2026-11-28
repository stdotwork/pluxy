package server

import (
	"net/netip"
	"sync"
	"time"
)

const (
	lockoutFailures = 5
	lockoutFor      = 30 * time.Minute
	lockoutIdle     = 2 * time.Hour
	lockoutSweep    = 10 * time.Minute
	lockoutClients  = 10000
)

type lockout struct {
	mu      sync.Mutex
	clients map[netip.Prefix]*attempts
	max     int
	swept   time.Time
	now     func() time.Time
}

type attempts struct {
	failures int
	last     time.Time
	until    time.Time
}

func newLockout() *lockout {
	return &lockout{clients: map[netip.Prefix]*attempts{}, max: lockoutClients, now: time.Now}
}

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
