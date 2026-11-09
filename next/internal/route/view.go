package route

import (
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Cooldown is one live block on an account, as the management API shows it.
type Cooldown struct {
	// Scope is what the block covers: credential (the whole account), model (one model on it)
	// or endpoint (every account that names the same endpoint).
	Scope string `json:"scope"`
	// Model is the model a model-scoped block covers.
	Model string `json:"model,omitempty"`
	// Kind is rate_limit, quota or cooldown (failures that gave no reset time).
	Kind  string    `json:"kind"`
	Until time.Time `json:"until"`
	// BackoffLevel is how far up the backoff ladder the block is, for blocks without a reset
	// time from the upstream.
	BackoffLevel int `json:"backoff_level,omitempty"`
	// Error is the failure that set the block.
	Error *llm.Error `json:"error,omitempty"`
}

// Runtime is what routing knows about one account now.
type Runtime struct {
	// Cooldowns are the account's live blocks, latest end first.
	Cooldowns []Cooldown
	// CooldownUntil is when the latest block that covers the whole account (its credential
	// or its endpoint) ends; zero when none does.
	CooldownUntil time.Time
	// LastError is the latest failure an attempt on the account recorded a block for, and
	// LastErrorAt when; nil once an attempt that started after it was recorded succeeds. As with
	// blocks, a success from an attempt that started earlier does not clear it, and a failure
	// that arrives after a success is recorded.
	LastError   *llm.Error
	LastErrorAt time.Time
	// Active counts the attempts running on the account.
	Active int
}

var blockKinds = [numBlockKinds]string{blockRateLimit, blockQuota, blockCooldown}

// Runtime returns what the state holds about account a at now. Errors are shown without the
// upstream's raw body and usage.
func (s *State) Runtime(a Account, now time.Time) Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rt Runtime
	for k, st := range s.scopes {
		if !s.covers(k, &a) {
			continue
		}
		for i, b := range st.blocks {
			if !b.until.After(now) {
				continue
			}
			rt.Cooldowns = append(rt.Cooldowns, Cooldown{Scope: k.kind, Model: k.model, Kind: blockKinds[i], Until: b.until,
				BackoffLevel: b.level, Error: Brief(b.reason)})
			if k.kind != scopeModel && b.until.After(rt.CooldownUntil) {
				rt.CooldownUntil = b.until
			}
		}
	}
	slices.SortFunc(rt.Cooldowns, func(x, y Cooldown) int {
		if c := y.Until.Compare(x.Until); c != 0 {
			return c
		}
		if c := strings.Compare(x.Scope, y.Scope); c != 0 {
			return c
		}
		if c := strings.Compare(x.Model, y.Model); c != 0 {
			return c
		}
		return strings.Compare(x.Kind, y.Kind)
	})
	if l, ok := s.last[a.ID]; ok {
		rt.LastError, rt.LastErrorAt = Brief(l.err), l.at
	}
	rt.Active = s.active[a.ID]
	return rt
}

// Reset clears every block that covers account a, on its credential, its models and its
// endpoint (which accounts naming the same endpoint share), with their backoff steps, and the
// account's last error. It reports how many live blocks it cleared. An attempt already running
// that fails afterwards records its block as usual.
func (s *State) Reset(a Account, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, st := range s.scopes {
		if !s.covers(k, &a) {
			continue
		}
		for _, b := range st.blocks {
			if b.until.After(now) {
				n++
			}
		}
		s.drop(k)
	}
	delete(s.last, a.ID)
	return n
}

// covers reports whether state kept on k is about account a. s.mu is held.
func (s *State) covers(k scopeKey, a *Account) bool {
	if k.kind == scopeEndpoint {
		return k.id == a.endpoint()
	}
	return k.id == a.ID
}

// Brief returns e without the upstream's raw body and usage, for listings.
func Brief(e *llm.Error) *llm.Error {
	if e == nil {
		return nil
	}
	c := *e
	c.Raw, c.Usage = nil, nil
	return &c
}
