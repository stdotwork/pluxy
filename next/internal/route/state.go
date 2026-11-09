package route

import (
	"container/heap"
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
	// mark is the sequence number of the latest-started attempt whose failure the block holds.
	// Results count in the order their attempts started: only a success from an attempt that
	// started after it (a higher sequence number) may clear the block, since an older success
	// says nothing about the newer failure.
	mark   uint64
	reason *llm.Error
	// told is true when until is a quota reset the upstream named, and window the length in
	// minutes of the usage window that ran out, when it said: the evidence the usage-limit
	// reply needs.
	told   bool
	window int64
}

type scopeState struct {
	blocks [numBlockKinds]block
	// won is the sequence number of the latest-started attempt that succeeded on the scope,
	// kept while an attempt that started before it is still running. That attempt's failure,
	// when it comes, is older news than the success and records nothing: a stale failure does
	// not block a scope that has since worked.
	won uint64
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

// State is core's routing state for accounts: blocks per scope, active attempts per account,
// session bindings and the rotation among accounts. It is shared by every request and safe for concurrent use. It is kept
// against account IDs and endpoint names, not against one config, so it outlives a reload.
type State struct {
	mu        sync.Mutex
	seq       uint64
	scopes    map[scopeKey]*scopeState
	models    int // model-scoped entries in scopes
	active    map[string]int
	affinity  *affinity
	rotation  *rotation
	lastSweep time.Time
	// gens counts the routers made over this state; each takes the next as its generation.
	gens uint64
	// since holds, per account ID and per endpoint name whose state Retain dropped, the
	// generation of the router Retain carried the state to: an outcome seen by an older router
	// is about the account as it was, so it records nothing.
	since map[scopeKey]uint64
	// last holds, per account ID, the latest failure an attempt on the account recorded a
	// block for, until an attempt that started after it was recorded succeeds (block.mark's rule).
	last map[string]lastFailure
	// running holds the sequence numbers of the attempts reserved and not yet ended, and order
	// the same numbers as a min-heap (with numbers already ended removed lazily), so the oldest
	// running attempt is found quickly.
	running map[uint64]struct{}
	order   seqHeap
	rand    func() float64
}

// seqHeap is a min-heap of attempt sequence numbers.
type seqHeap []uint64

func (h seqHeap) Len() int           { return len(h) }
func (h seqHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h seqHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *seqHeap) Push(x any)        { *h = append(*h, x.(uint64)) }
func (h *seqHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// oldest returns the sequence number of the oldest running attempt, or 0 when none runs.
// Call with s.mu held.
func (s *State) oldest() uint64 {
	for len(s.order) > 0 {
		if _, ok := s.running[s.order[0]]; ok {
			return s.order[0]
		}
		heap.Pop(&s.order)
	}
	return 0
}

// needed reports whether st must be kept: a block or backoff step is live within the idle
// window, or its won mark may still turn away the failure of an older running attempt. Call
// with s.mu held.
func (s *State) needed(st *scopeState, cutoff time.Time) bool {
	if st.lastEnd().After(cutoff) {
		return true
	}
	o := s.oldest()
	return st.won != 0 && o != 0 && o < st.won
}

// lastFailure is the latest failure recorded about an account.
type lastFailure struct {
	at   time.Time
	mark uint64 // as block.mark
	err  *llm.Error
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

// sweep drops entries whose blocks all ended before cutoff and whose won mark no running
// attempt is older than. Call with s.mu held.
func (s *State) sweep(cutoff time.Time) {
	for k, st := range s.scopes {
		if !s.needed(st, cutoff) {
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
	s.running[s.seq] = struct{}{}
	heap.Push(&s.order, s.seq)
	return s.seq, true
}

// NewState returns empty state.
func NewState() *State {
	return &State{scopes: map[scopeKey]*scopeState{}, active: map[string]int{}, affinity: newAffinity(), rotation: newRotation(),
		since: map[scopeKey]uint64{}, last: map[string]lastFailure{}, running: map[uint64]struct{}{}, rand: rand.Float64}
}

// nextGen returns the generation for a new router.
func (s *State) nextGen() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gens++
	return s.gens
}

// Retain carries the state from the accounts of one router (prev, the one a reload replaces)
// to those of the next, which must not be serving yet. State stays with every account of next
// that same reports is the same account as the one with its ID in prev. For accounts that were
// removed or changed it is dropped: blocks on their credential and models, their session
// bindings, and blocks on endpoints that no unchanged account names: a block is about one
// upstream account, and a changed account may be another one. From then on, outcomes that
// routers older than next see on what it dropped record nothing and bind no session, whenever
// their attempts began: a request that started on an older router may still retry there.
// Weighted rotation starts afresh when the weights change, and older routers keep their own
// balances. Running attempts stay counted until they end.
func (s *State) Retain(prevRouter, nextRouter *Router, same func(old, cur *Account) bool) {
	prev, next, gen := prevRouter.Accounts(), nextRouter.Accounts(), nextRouter.gen
	kept := map[string]bool{}
	endpoints := map[string]bool{}
	weights := map[string]int{}
	for i := range next {
		a := &next[i]
		weights[a.ID] = a.weight()
		for j := range prev {
			if prev[j].ID == a.ID && same(&prev[j], a) {
				kept[a.ID] = true
				endpoints[a.endpoint()] = true
			}
		}
	}
	reweighted := len(prev) != len(next)
	for i := range prev {
		if w, ok := weights[prev[i].ID]; !ok || w != prev[i].weight() {
			reweighted = true
		}
	}

	changed := map[string]bool{}
	s.mu.Lock()
	for _, a := range prev {
		if !kept[a.ID] {
			changed[a.ID] = true
			s.since[scopeKey{kind: scopeCredential, id: a.ID}] = gen
		}
		if !endpoints[a.endpoint()] {
			s.since[scopeKey{kind: scopeEndpoint, id: a.endpoint()}] = gen
		}
	}
	for k := range s.scopes {
		if k.kind == scopeEndpoint && !endpoints[k.id] || k.kind != scopeEndpoint && !kept[k.id] {
			s.drop(k)
		}
	}
	for id := range s.last {
		if !kept[id] {
			delete(s.last, id)
		}
	}
	s.mu.Unlock()

	s.affinity.retain(kept, changed, gen)
	if reweighted {
		s.rotation.mu.Lock()
		clear(s.rotation.current)
		s.rotation.weightsSince = gen
		s.rotation.mu.Unlock()
	}
}

// stale reports whether an outcome on key seen by a router of generation gen is about an
// account as it was before Retain dropped key's state. s.mu is held.
func (s *State) stale(key scopeKey, gen uint64) bool {
	if key.kind == scopeModel {
		key = scopeKey{kind: scopeCredential, id: key.id}
	}
	return gen < s.since[key]
}

// end releases the reservation reserve made for the attempt with sequence number seq on
// account a. Call it exactly once per reservation, after recording the attempt's outcome.
func (s *State) end(a *Account, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, seq)
	if len(s.order) > 2*len(s.running)+64 {
		// Numbers that ended under a long-running older attempt pile up behind it: rebuild.
		s.order = s.order[:0]
		for n := range s.running {
			s.order = append(s.order, n)
		}
		heap.Init(&s.order)
	}
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
	delete(s.last, id)
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

// failure records what the failed attempt with sequence number seq on account a for model says
// about the account. Failures about the request or the caller record nothing, and so does a
// failure on a scope where an attempt that started after this one has already succeeded: the
// newest result wins. transient replaces the backoff for a failing upstream or endpoint: above
// 0 it is the block's length, below 0 no block is kept.
func (s *State) failure(a *Account, model string, seq uint64, e *llm.Error, now time.Time, transient time.Duration) {
	kind, scope, fixed, base, max := classify(e)
	if kind == "" {
		return
	}
	if e.Code == llm.CodeUpstream || e.Code == llm.CodeUnavailable {
		switch {
		case transient < 0:
			return
		case transient > 0:
			fixed = transient
		}
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
	if s.stale(key, a.gen) {
		return
	}
	st := s.scopes[key]
	if st != nil && st.won > seq {
		return // a newer attempt has succeeded on this scope since this one started
	}
	s.maybeSweep(now)
	st = s.scopes[key]
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
	// The block is kept, so the failure is the account's last error, unless an attempt on the
	// account that started after this one has since succeeded.
	if cred := s.scopes[scopeKey{kind: scopeCredential, id: a.ID}]; cred == nil || cred.won <= seq {
		if l, ok := s.last[a.ID]; !ok || l.mark <= seq {
			s.last[a.ID] = lastFailure{at: now, mark: seq, err: e}
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
		b.told = kind == blockQuota && e.RetryAfterMS > 0
		b.window = e.QuotaWindowMinutes
	}
	if seq >= b.mark {
		b.mark, b.reason = seq, e
	}
}

// quotaOut reports whether what keeps account a from serving model at now, its block that ends
// last, is a quota block whose end the upstream named: then until is that end, window the
// length of the window that ran out (0 when not known), and reason the failure that set it.
func (s *State) quotaOut(a *Account, model string, now time.Time) (until time.Time, window int64, reason *llm.Error, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last block
	kind := -1
	for _, k := range keysFor(a, model) {
		st := s.scopes[k]
		if st == nil {
			continue
		}
		for i, b := range st.blocks {
			if b.until.After(now) && b.until.After(last.until) {
				last, kind = b, i
			}
		}
	}
	if kind != kindIndex(blockQuota) || !last.told {
		return time.Time{}, 0, nil, false
	}
	return last.until, last.window, last.reason, true
}

// success records that the attempt with sequence number seq on account a for model succeeded.
// It clears blocks on the attempt's scopes that failures of attempts started before it left,
// and, while such an attempt is still running, marks the scopes so that its failure records
// nothing when it comes.
func (s *State) success(a *Account, model string, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.last[a.ID]; ok && l.mark < seq && !s.stale(scopeKey{kind: scopeCredential, id: a.ID}, a.gen) {
		delete(s.last, a.ID)
	}
	o := s.oldest()
	older := o != 0 && o < seq // an attempt that started before this one is still running
	for _, k := range keysFor(a, model) {
		if s.stale(k, a.gen) {
			continue
		}
		st := s.scopes[k]
		if st == nil {
			if !older {
				continue
			}
			if k.kind == scopeModel && s.models >= maxModelKeys {
				// shortcut: with the table full of live model blocks the mark is not kept, so
				// an older attempt's failure on this model may still block it; raise
				// maxModelKeys if real pools serve more models than this at once.
				continue
			}
			st = &scopeState{}
			s.scopes[k] = st
			if k.kind == scopeModel {
				s.models++
			}
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
		if older && seq > st.won {
			st.won = seq
		}
		if empty && !s.needed(st, time.Time{}) {
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
