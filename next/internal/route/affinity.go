package route

import (
	"sync"
	"time"
)

// affinity remembers which account served a session, so later requests of that session go to
// the same account while it stays eligible and keep using its prompt cache. It is a soft
// preference: when the bound account is blocked the request goes elsewhere and the session is
// bound to the new account. Bindings are scoped by tenant (the client key) and model and
// expire after the router's TTL without use. The table is part of State, so bindings survive a
// config reload.
type affinity struct {
	mu       sync.Mutex
	bindings map[affinityKey]binding
}

type affinityKey struct{ tenant, model, session string }

type binding struct {
	account string
	expires time.Time
}

// maxBindings bounds the table. shortcut: when it is full of live bindings, new sessions go
// unbound until some expire; a least-recently-used eviction would keep binding new sessions,
// worth adding if pools see more concurrent sessions than this.
const maxBindings = 100_000

func newAffinity() *affinity {
	return &affinity{bindings: map[affinityKey]binding{}}
}

// lookup returns the account bound to the session, if any.
func (f *affinity) lookup(k affinityKey, now time.Time) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bindings[k]
	if !ok || !b.expires.After(now) {
		return "", false
	}
	return b.account, true
}

// bind binds the session to account until ttl from now.
func (f *affinity) bind(k affinityKey, account string, now time.Time, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.bindings[k]; !ok && len(f.bindings) >= maxBindings {
		for key, b := range f.bindings {
			if !b.expires.After(now) {
				delete(f.bindings, key)
			}
		}
		if len(f.bindings) >= maxBindings {
			return
		}
	}
	f.bindings[k] = binding{account: account, expires: now.Add(ttl)}
}

// retain drops the bindings to accounts not in keep.
func (f *affinity) retain(keep map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, b := range f.bindings {
		if !keep[b.account] {
			delete(f.bindings, k)
		}
	}
}
