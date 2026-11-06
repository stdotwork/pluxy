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

// Bounds on state keyed by model names, which clients choose.
var (
	// maxModelKeys bounds the model-scoped entries kept (blocks here, rotation cursors in the
	// picker). When it is full of live blocks, a new model-scoped block is not recorded: live
	// cooldowns are never dropped to make room.
	maxModelKeys = 10_000
	// stateIdle: an entry whose blocks all ended this long ago is dropped (its backoff step
	// with it), as is a rotation cursor unused this long.
	stateIdle = time.Hour
	// sweepEvery is how often idle entries are looked for.
	sweepEvery = time.Minute
)

// State is core's routing state for accounts: blocks per scope and active attempts per
// account. It is shared by every request and safe for concurrent use.
type State struct {
	mu        sync.Mutex
	seq       uint64
	scopes    map[scopeKey]*scopeState
	models    int // model-scoped entries in scopes
	active    map[string]int
	rand      func() float64
	lastSweep time.Time
}

// lastEnd returns when the latest block of st ends.
func (st *scopeState) lastEnd() time.Time {
	var t time.Time
	for _, b := range st.blocks {
		if b.until.After(t) {
			t = b.until
		}
	}
	return t
}

// drop removes the entry for k. Call with s.mu held.
func (s *State) drop(k scopeKey) {
	if _, ok := s.scopes[k]; !ok {
		return
	}
	delete(s.scopes, k)
	if k.kind == scopeModel {
		s.models--
	}
}

// sweep drops entries whose blocks all ended before cutoff. Call with s.mu held.
func (s *State) sweep(cutoff time.Time) {
	for k, st := range s.scopes {
		if !st.lastEnd().After(cutoff) {
			s.drop(k)
		}
	}
}

// maybeSweep drops idle entries, at most once per sweepEvery. Call with s.mu held.
func (s *State) maybeSweep(now time.Time) {
	if now.Sub(s.lastSweep) < sweepEvery {
		return
	}
	s.lastSweep = now
	s.sweep(now.Add(-stateIdle))
}

// reserve checks, in one step with reserving it, that account a may serve model now: no block
// covers it and it is under its concurrency limit. Two requests that both chose a can then
// never both run on it beyond its limit. It returns the attempt's sequence number.
func (s *State) reserve(a *Account, model string, now time.Time) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeSweep(now)
	for _, k := range keysFor(a, model) {
		if st := s.scopes[k]; st != nil && st.lastEnd().After(now) {
			return 0, false
		}
	}
	if a.MaxConcurrency > 0 && s.active[a.ID] >= a.MaxConcurrency {
		return 0, false
	}
	s.seq++
	s.active[a.ID]++
	return s.seq, true
}

// NewState returns empty state.
func NewState() *State {
	return &State{scopes: map[scopeKey]*scopeState{}, active: map[string]int{}, rand: rand.Float64}
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

// forget drops the state kept on account id's credential and models. Endpoint state stays: other
// accounts may share the endpoint.
func (s *State) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.scopes {
		if k.kind != scopeEndpoint && k.id == id {
			s.drop(k)
		}
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
	s.maybeSweep(now)
	st := s.scopes[key]
	if st == nil {
		if key.kind == scopeModel && s.models >= maxModelKeys {
			s.sweep(now) // make room from blocks that have ended
			if s.models >= maxModelKeys {
				// shortcut: the table is full of live model blocks, so this one is not kept
				// (the next request for the model tries the account again); raise
				// maxModelKeys if real pools serve more models than this at once.
				return
			}
		}
		st = &scopeState{}
		s.scopes[key] = st
		if key.kind == scopeModel {
			s.models++
		}
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
			s.drop(k)
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
