package route

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Scope kinds: what a piece of state is about. A credential scope covers one account's
// credential across all its models; a model scope covers one model on one account; an endpoint
// scope covers every account that names the same endpoint.
const (
	scopeCredential = llm.ScopeCredential
	scopeModel      = llm.ScopeModel
	scopeEndpoint   = llm.ScopeEndpoint
)

type scopeKey struct {
	kind  string
	id    string // account ID, or endpoint name for endpoint scopes
	model string // model scopes only
}

// Block kinds. Each is kept separately on its scope, so a quota observation is never mistaken
// for a short rate limit and a transient-failure backoff never erases a known quota reset.
const (
	blockRateLimit = "rate_limit" // the upstream refused for request rate; resets soon
	blockQuota     = "quota"      // a usage allowance is used up
	blockCooldown  = "cooldown"   // failures without a reset time: backoff
	numBlockKinds  = 3
)

func kindIndex(kind string) int {
	switch kind {
	case blockRateLimit:
		return 0
	case blockQuota:
		return 1
	default:
		return 2
	}
}

// block says a scope may not be used before until.
type block struct {
	until time.Time
	// level is the backoff exponent for blocks without a reset time from the upstream.
	level int
	// mark is the attempt sequence number current when the block was recorded. Only a success
	// from an attempt that started after it (a higher sequence number) may clear the block: an
	// older success says nothing about the newer failure.
	mark   uint64
	reason *llm.Error
}

type scopeState struct {
	blocks [numBlockKinds]block
}

// Backoff settings for failures that carry no reset time.
var (
	// Rate limits and quotas without a reset time: 1s doubling to 30 minutes, the ladder
	// today's core uses.
	limitBackoffBase = time.Second
	limitBackoffMax  = 30 * time.Minute
	// Transient upstream and endpoint failures: 1s doubling to 1 minute.
	failureBackoffBase = time.Second
	failureBackoffMax  = time.Minute
	// A refused credential is left alone for 30 minutes; a model the account does not have,
	// for 12 hours. Both match today's core.
	authCooldown     = 30 * time.Minute
	notFoundCooldown = 12 * time.Hour
)

// State is core's routing state for accounts: blocks per scope and active attempts per
// account. It is shared by every request and safe for concurrent use.
type State struct {
	mu     sync.Mutex
	seq    uint64
	scopes map[scopeKey]*scopeState
	active map[string]int
	rand   func() float64
}

// NewState returns empty state.
func NewState() *State {
	return &State{scopes: map[scopeKey]*scopeState{}, active: map[string]int{}, rand: rand.Float64}
}

// begin reserves an attempt on account a and returns the attempt's sequence number.
func (s *State) begin(a *Account) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.active[a.ID]++
	return s.seq
}

// end releases the reservation begin made. Call it exactly once per begin.
func (s *State) end(a *Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[a.ID] <= 1 {
		delete(s.active, a.ID)
	} else {
		s.active[a.ID]--
	}
}

func (s *State) activeCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id]
}

func keysFor(a *Account, model string) [3]scopeKey {
	return [3]scopeKey{
		{kind: scopeCredential, id: a.ID},
		{kind: scopeModel, id: a.ID, model: model},
		{kind: scopeEndpoint, id: a.endpoint()},
	}
}

// availability reports when account a can next serve model: the zero time when it can now,
// and the error that blocks it otherwise. busy is true when the account is at its concurrency
// limit.
func (s *State) availability(a *Account, model string, now time.Time) (until time.Time, reason *llm.Error, busy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keysFor(a, model) {
		st := s.scopes[k]
		if st == nil {
			continue
		}
		for _, b := range st.blocks {
			if b.until.After(now) && b.until.After(until) {
				until, reason = b.until, b.reason
			}
		}
	}
	busy = a.MaxConcurrency > 0 && s.active[a.ID] >= a.MaxConcurrency
	return until, reason, busy
}

// failure records what a failed attempt on account a for model says about the account.
// Failures about the request or the caller record nothing.
func (s *State) failure(a *Account, model string, e *llm.Error, now time.Time) {
	kind, scope, fixed, base, max := classify(e)
	if kind == "" {
		return
	}
	var key scopeKey
	switch scope {
	case scopeCredential:
		key = scopeKey{kind: scopeCredential, id: a.ID}
	case scopeModel:
		key = scopeKey{kind: scopeModel, id: a.ID, model: model}
	case scopeEndpoint:
		key = scopeKey{kind: scopeEndpoint, id: a.endpoint()}
	default:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.scopes[key]
	if st == nil {
		st = &scopeState{}
		s.scopes[key] = st
	}
	b := &st.blocks[kindIndex(kind)]
	var until time.Time
	switch {
	case e.RetryAfterMS > 0:
		// The upstream said when: never try this scope earlier (RFC 9110 §10.2.3).
		until = now.Add(time.Duration(e.RetryAfterMS) * time.Millisecond)
	case fixed > 0:
		until = now.Add(fixed)
	default:
		// No reset time: capped exponential backoff. Failures that land while the previous
		// block is still live share its step, so a burst of concurrent failures climbs the
		// ladder once, not once per request.
		step := b.level
		if b.until.After(now) && step > 0 {
			step--
		}
		d := base << step
		if d <= 0 || d >= max {
			d = max
			b.level = step // at the cap: stop climbing
		} else {
			b.level = step + 1
		}
		// Equal jitter: between half and all of the step, so accounts that failed together
		// do not all come back at the same instant.
		d = d/2 + time.Duration(s.rand()*float64(d/2))
		until = now.Add(d)
	}
	// A deadline only ever moves later; a new failure never shortens a live block.
	if until.After(b.until) {
		b.until = until
	}
	b.mark = s.seq
	b.reason = e
}

// success records that the attempt with sequence number seq on account a for model succeeded.
// It clears blocks on the attempt's scopes, except blocks recorded after the attempt started.
func (s *State) success(a *Account, model string, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keysFor(a, model) {
		st := s.scopes[k]
		if st == nil {
			continue
		}
		empty := true
		for i := range st.blocks {
			b := &st.blocks[i]
			if b.mark < seq {
				*b = block{}
			}
			if !b.until.IsZero() || b.level > 0 {
				empty = false
			}
		}
		if empty {
			delete(s.scopes, k)
		}
	}
}

// classify reads an extension's typed outcome: which kind of block it means, on which scope,
// and for how long when the upstream gave no reset time (a fixed duration, or a backoff ladder
// from base to max). An empty kind means the failure says nothing about the account.
func classify(e *llm.Error) (kind, scope string, fixed, base, max time.Duration) {
	scope = e.Scope
	switch e.Code {
	case llm.CodeRateLimit:
		kind, base, max = blockRateLimit, limitBackoffBase, limitBackoffMax
		if scope == "" {
			scope = scopeCredential
		}
	case llm.CodeQuota:
		kind, base, max = blockQuota, limitBackoffBase, limitBackoffMax
		if scope == "" {
			scope = scopeCredential
		}
	case llm.CodeAuth, llm.CodePermission:
		kind, fixed = blockCooldown, authCooldown
		if scope == "" {
			scope = scopeCredential
		}
	case llm.CodeNotFound:
		kind, fixed = blockCooldown, notFoundCooldown
		if scope == "" {
			scope = scopeModel
		}
	case llm.CodeUpstream, llm.CodeUnavailable:
		kind, base, max = blockCooldown, failureBackoffBase, failureBackoffMax
		if scope == "" {
			scope = scopeEndpoint
		}
	default:
		// invalid_request, unsupported, cancelled, internal and core's extension_* codes are
		// about the request, the caller or the extension process, not the account.
		return "", "", 0, 0, 0
	}
	if scope == llm.ScopeRequest {
		return "", "", 0, 0, 0
	}
	return kind, scope, fixed, base, max
}
