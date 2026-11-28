package route

import (
	"container/heap"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	scopeCredential = llm.ScopeCredential
	scopeModel      = llm.ScopeModel
	scopeEndpoint   = llm.ScopeEndpoint
)

type scopeKey struct {
	kind  string
	id    string
	model string
}

const (
	blockRateLimit = "rate_limit"
	blockQuota     = "quota"
	blockCooldown  = "cooldown"
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

type block struct {
	until     time.Time
	level     int
	mark      uint64
	reason    *llm.Error
	told      bool
	window    int64
	source    string
	exhausted bool
	check     time.Time
	pass      bool
	why       string
}

func (b *block) live(now time.Time) bool { return b.until.After(now) && !b.pass }

type scopeState struct {
	blocks [numBlockKinds]block
	won    uint64
}

var (
	limitBackoffBase   = time.Second
	limitBackoffMax    = 30 * time.Minute
	failureBackoffBase = time.Second
	failureBackoffMax  = time.Minute
	authCooldown       = 30 * time.Minute
	notFoundCooldown   = 12 * time.Hour
)

var (
	maxModelKeys = 10_000
	stateIdle    = time.Hour
	sweepEvery   = time.Minute
)

type State struct {
	keyMu  sync.RWMutex
	pubKey []byte

	mu          sync.Mutex
	seq         uint64
	scopes      map[scopeKey]*scopeState
	models      int
	active      map[string]int
	affinity    *affinity
	rotation    *rotation
	lastSweep   time.Time
	gens        uint64
	since       map[scopeKey]uint64
	revs        map[string]uint64
	last        map[string]lastFailure
	running     map[uint64]struct{}
	order       seqHeap
	rand        func() float64
	checkEvery  time.Duration
	bans        map[string]banState
	unbannedSeq map[string]uint64
	changes     chan struct{}
	usage       map[string]usageReading
	refreshed   time.Time
	quotas      map[string]QuotaReading
}

type banState struct {
	why    string
	mark   uint64
	at     time.Time
	reason *llm.Error
}

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

func (s *State) oldest() uint64 {
	for len(s.order) > 0 {
		if _, ok := s.running[s.order[0]]; ok {
			return s.order[0]
		}
		heap.Pop(&s.order)
	}
	return 0
}

func (s *State) needed(st *scopeState, cutoff time.Time) bool {
	if st.lastEnd().After(cutoff) {
		return true
	}
	o := s.oldest()
	return st.won != 0 && o != 0 && o < st.won
}

type lastFailure struct {
	at   time.Time
	mark uint64
	err  *llm.Error
}

func (st *scopeState) lastEnd() time.Time {
	var t time.Time
	for _, b := range st.blocks {
		if b.until.After(t) {
			t = b.until
		}
	}
	return t
}

func (s *State) drop(k scopeKey) {
	st, ok := s.scopes[k]
	if !ok {
		return
	}
	if k.kind != scopeEndpoint && !st.lastEnd().IsZero() {
		s.notify()
	}
	delete(s.scopes, k)
	if k.kind == scopeModel {
		s.models--
	}
}

func (s *State) sweep(cutoff time.Time) {
	for k, st := range s.scopes {
		if !s.needed(st, cutoff) {
			s.drop(k)
		}
	}
}

func (s *State) maybeSweep(now time.Time) {
	if now.Sub(s.lastSweep) < sweepEvery {
		return
	}
	s.lastSweep = now
	s.sweep(now.Add(-stateIdle))
}

func (s *State) reserve(a *Account, model string, now time.Time) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, banned := s.bans[a.ID]; banned {
		return 0, false
	}
	s.maybeSweep(now)
	keys := keysFor(a, model)
	for _, k := range keys {
		if st := s.scopes[k]; st != nil {
			for i := range st.blocks {
				if st.blocks[i].live(now) {
					return 0, false
				}
			}
		}
	}
	if a.MaxConcurrency > 0 && s.active[a.ID] >= a.MaxConcurrency {
		return 0, false
	}
	for _, k := range keys {
		if st := s.scopes[k]; st != nil {
			for i := range st.blocks {
				if b := &st.blocks[i]; b.pass && b.until.After(now) {
					b.pass, b.check = false, s.nextCheck(b.until, now)
				}
			}
		}
	}
	s.seq++
	s.active[a.ID]++
	s.running[s.seq] = struct{}{}
	heap.Push(&s.order, s.seq)
	return s.seq, true
}

func NewState() *State {
	return &State{scopes: map[scopeKey]*scopeState{}, active: map[string]int{}, affinity: newAffinity(), rotation: newRotation(),
		since: map[scopeKey]uint64{}, revs: map[string]uint64{}, last: map[string]lastFailure{}, running: map[uint64]struct{}{}, rand: rand.Float64,
		bans: map[string]banState{}, unbannedSeq: map[string]uint64{}, usage: map[string]usageReading{},
		quotas: map[string]QuotaReading{}, checkEvery: defaultCheckEvery, changes: make(chan struct{}, 1), pubKey: NewPublicKey()}
}

func (s *State) RetainBans(known func(id string) bool) {
	if known == nil {
		return
	}
	s.mu.Lock()
	if len(s.bans) == 0 {
		s.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(s.bans))
	for id := range s.bans {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	var drop []string
	for _, id := range ids {
		if !known(id) {
			drop = append(drop, id)
		}
	}
	if len(drop) == 0 {
		return
	}
	s.mu.Lock()
	changed := false
	for _, id := range drop {
		if ban, ok := s.bans[id]; ok && !known(id) {
			delete(s.bans, id)
			s.unbannedSeq[id] = max(s.unbannedSeq[id], s.seq, ban.mark)
			changed = true
		}
	}
	if changed {
		s.notify()
	}
	s.mu.Unlock()
}

func (s *State) IsBanned(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.bans[id]
	return ok
}

func (s *State) banReason(id string) *llm.Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bans[id].reason
}

func (s *State) Unban(id string) bool {
	s.mu.Lock()
	ban, wasBanned := s.bans[id]
	if !wasBanned {
		s.mu.Unlock()
		return false
	}
	delete(s.bans, id)
	delete(s.last, id)
	for k := range s.scopes {
		if k.kind != scopeEndpoint && k.id == id {
			s.drop(k)
		}
	}
	s.unbannedSeq[id] = max(s.unbannedSeq[id], s.seq, ban.mark)
	s.notify()
	s.mu.Unlock()
	log.Printf("routing: account %q unbanned", id)
	return true
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func (s *State) nextGen() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gens++
	return s.gens
}

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
	for id := range s.usage {
		if !kept[id] {
			delete(s.usage, id)
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

func (s *State) stale(key scopeKey, a *Account) bool {
	if a.rev < s.revs[a.ID] {
		return true
	}
	if key.kind == scopeModel {
		key = scopeKey{kind: scopeCredential, id: key.id}
	}
	return a.gen < s.since[key]
}

func (s *State) revision(id string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revs[id]
}

func (s *State) restart(old, fresh []*Account, kept map[string]bool) {
	if len(old) == 0 {
		return
	}
	revs, endpoints := map[string]uint64{}, map[string]bool{}
	s.mu.Lock()
	for _, a := range old {
		s.revs[a.ID]++
		revs[a.ID] = s.revs[a.ID]
		if !kept[a.endpoint()] {
			endpoints[a.endpoint()] = true
		}
	}
	for k := range s.scopes {
		if _, ok := revs[k.id]; k.kind == scopeEndpoint && endpoints[k.id] || k.kind != scopeEndpoint && ok {
			s.drop(k)
		}
	}
	for id := range revs {
		delete(s.last, id)
		delete(s.usage, id)
	}
	s.mu.Unlock()
	for _, a := range fresh {
		a.rev = revs[a.ID]
	}
	s.affinity.unbind(revs)
}

func (s *State) end(a *Account, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, seq)
	if len(s.order) > 2*len(s.running)+64 {
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

func (s *State) forget(id string) {
	s.mu.Lock()
	for k := range s.scopes {
		if k.kind != scopeEndpoint && k.id == id {
			s.drop(k)
		}
	}
	delete(s.last, id)
	delete(s.usage, id)
	s.revs[id]++
	rev := s.revs[id]
	s.mu.Unlock()
	s.affinity.unbind(map[string]uint64{id: rev})
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

func (s *State) availability(a *Account, model string, now time.Time) (until time.Time, reason *llm.Error, busy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keysFor(a, model) {
		st := s.scopes[k]
		if st == nil {
			continue
		}
		for _, b := range st.blocks {
			if b.live(now) && b.until.After(until) {
				until, reason = b.until, b.reason
			}
		}
	}
	busy = a.MaxConcurrency > 0 && s.active[a.ID] >= a.MaxConcurrency
	return until, reason, busy
}

func (s *State) failure(a *Account, model string, seq uint64, e *llm.Error, now time.Time, transient time.Duration) {
	if e == nil {
		return
	}
	if e.Code == llm.CodeAccountClosed {
		s.mu.Lock()
		if s.stale(scopeKey{kind: scopeCredential, id: a.ID}, a) || (seq > 0 && seq <= s.unbannedSeq[a.ID]) {
			s.mu.Unlock()
			return
		}
		if _, banned := s.bans[a.ID]; banned {
			s.mu.Unlock()
			return
		}
		why := strings.TrimSpace(e.Message)
		if why == "" {
			why = "the provider closed or suspended the account"
		}
		s.bans[a.ID] = banState{why: why, mark: seq, at: now.UTC(), reason: e}
		s.last[a.ID] = lastFailure{at: now, mark: seq, err: e}
		for k := range s.scopes {
			if k.kind != scopeEndpoint && k.id == a.ID {
				s.drop(k)
			}
		}
		s.notify()
		s.mu.Unlock()
		log.Printf("error: routing: account %q banned: %s", a.ID, singleLine(why))
		return
	}
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
	if s.stale(key, a) || (seq > 0 && seq <= s.unbannedSeq[a.ID]) {
		return
	}
	if _, banned := s.bans[a.ID]; banned {
		return
	}
	st := s.scopes[key]
	if st != nil && st.won > seq {
		return
	}
	s.maybeSweep(now)
	st = s.scopes[key]
	if st == nil {
		if key.kind == scopeModel && s.models >= maxModelKeys {
			s.sweep(now)
			if s.models >= maxModelKeys {
				return
			}
		}
		st = &scopeState{}
		s.scopes[key] = st
		if key.kind == scopeModel {
			s.models++
		}
	}
	saved := key.kind != scopeEndpoint
	if cred := s.scopes[scopeKey{kind: scopeCredential, id: a.ID}]; cred == nil || cred.won <= seq {
		if l, ok := s.last[a.ID]; !ok || l.mark <= seq {
			s.last[a.ID] = lastFailure{at: now, mark: seq, err: e}
			saved = true
		}
	}
	b := &st.blocks[kindIndex(kind)]
	var until time.Time
	source, why, word := SourceBackoff, "", false
	switch {
	case e.RetryAfterMS > 0:
		var d time.Duration
		// Never try this scope earlier (RFC 9110 §10.2.3).
		d, source = s.resetFor(e.RetryAfterMS, e.QuotaWindowMinutes)
		if source == SourceUnverified {
			why = unverifiedWhy
		}
		until, word = now.Add(d), true
	case fixed > 0:
		until = now.Add(fixed)
	default:
		step := b.level
		if b.until.After(now) && step > 0 {
			step--
		}
		d := base << step
		if d <= 0 || d >= max {
			d = max
			b.level = step
		} else {
			b.level = step + 1
		}
		d = d/2 + time.Duration(s.rand()*float64(d/2))
		until = now.Add(d)
	}
	if word && seq >= b.mark || until.After(b.until) {
		s.set(b, key, kind, until, source, why, now)
		b.window = e.QuotaWindowMinutes
	}
	if seq >= b.mark {
		b.mark, b.reason = seq, e
	}
	if saved {
		s.notify()
	}
}

func (s *State) quotaOut(a *Account, model string, now time.Time) (until time.Time, window int64, reason *llm.Error, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := keysFor(a, model)
	if b, ok := s.quotaLast(keys[:1], now); ok {
		return b.until, b.window, b.reason, true
	}
	if b, ok := s.quotaLast(keys[:], now); ok {
		return b.until, b.window, b.reason, true
	}
	return time.Time{}, 0, nil, false
}

func (s *State) quotaLast(keys []scopeKey, now time.Time) (last block, ok bool) {
	var other time.Time
	for _, k := range keys {
		st := s.scopes[k]
		if st == nil {
			continue
		}
		for i, b := range st.blocks {
			switch {
			case !b.until.After(now):
			case i == kindIndex(blockQuota) && b.told:
				if b.until.After(last.until) {
					last = b
				}
			case b.until.After(other):
				other = b.until
			}
		}
	}
	return last, !last.until.IsZero() && last.until.After(other)
}

func (s *State) success(a *Account, model string, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, banned := s.bans[a.ID]; banned {
		return
	}
	if l, ok := s.last[a.ID]; ok && l.mark < seq && !s.stale(scopeKey{kind: scopeCredential, id: a.ID}, a) {
		delete(s.last, a.ID)
		s.notify()
	}
	o := s.oldest()
	older := o != 0 && o < seq
	for _, k := range keysFor(a, model) {
		if s.stale(k, a) {
			continue
		}
		st := s.scopes[k]
		if st == nil {
			if !older {
				continue
			}
			if k.kind == scopeModel && s.models >= maxModelKeys {
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
				if k.kind != scopeEndpoint && !b.until.IsZero() {
					s.notify()
				}
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

func classify(e *llm.Error) (kind, scope string, fixed, base, max time.Duration) {
	scope = e.Scope
	switch e.Code {
	case llm.CodeRateLimit:
		kind, base, max = blockRateLimit, limitBackoffBase, limitBackoffMax
		if scope == "" {
			scope = scopeModel
		}
	case llm.CodeQuota:
		kind, base, max = blockQuota, limitBackoffBase, limitBackoffMax
		if scope == "" {
			scope = scopeModel
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
	case llm.CodeUpstream, llm.CodeUnavailable, llm.CodeExtensionTimeout:
		kind, base, max = blockCooldown, failureBackoffBase, failureBackoffMax
		if scope == "" {
			scope = scopeEndpoint
		}
	default:
		return "", "", 0, 0, 0
	}
	if scope == llm.ScopeRequest {
		return "", "", 0, 0, 0
	}
	return kind, scope, fixed, base, max
}
