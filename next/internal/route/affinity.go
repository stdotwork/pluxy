package route

import (
	"sync"
	"time"
)

type affinity struct {
	mu       sync.Mutex
	bindings map[affinityKey]binding
	since    map[string]uint64
	revs     map[string]uint64
}

type affinityKey struct{ tenant, model, session string }

type binding struct {
	account string
	model   string
	expires time.Time
}

const maxBindings = 100_000

func newAffinity() *affinity {
	return &affinity{bindings: map[affinityKey]binding{}, since: map[string]uint64{}, revs: map[string]uint64{}}
}

func (f *affinity) lookup(k affinityKey, now time.Time) (binding, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bindings[k]
	if !ok || !b.expires.After(now) {
		return binding{}, false
	}
	return b, true
}

func (f *affinity) bind(k affinityKey, a *Account, model string, now time.Time, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.gen < f.since[a.ID] || a.rev < f.revs[a.ID] {
		return
	}
	account := a.ID
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
	f.bindings[k] = binding{account: account, model: model, expires: now.Add(ttl)}
}

func (f *affinity) retain(keep, changed map[string]bool, gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range changed {
		f.since[id] = gen
	}
	for k, b := range f.bindings {
		if !keep[b.account] {
			delete(f.bindings, k)
		}
	}
}

func (f *affinity) unbind(revs map[string]uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, rev := range revs {
		f.revs[id] = rev
	}
	for k, b := range f.bindings {
		if _, ok := revs[b.account]; ok {
			delete(f.bindings, k)
		}
	}
}
