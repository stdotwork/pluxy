package route

import (
	"sync"
	"time"
)

// affinity remembers which account served a session, so later requests of that session go to
// the same account while it stays eligible and keep using its prompt cache. It is a soft
// preference: when the bound account is blocked the request goes elsewhere and the session is
// bound to the new account. Bindings are scoped by tenant (the client key) and model and
// expire after ttl without use.
type affinity struct {
	ttl time.Duration

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

func newAffinity(ttl time.Duration) *affinity {
	return &affinity{ttl: ttl, bindings: map[affinityKey]binding{}}
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

// bind binds the session to account and restarts its expiry.
func (f *affinity) bind(k affinityKey, account string, now time.Time) {
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
	f.bindings[k] = binding{account: account, expires: now.Add(f.ttl)}
}
