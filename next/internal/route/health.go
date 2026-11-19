package route

import (
	"cmp"
	"strings"
	"time"
)

// Health is one account's state in one word, with when and why: what the management API shows
// as an account's health. It is derived from the blocks routing keeps on the account (its
// credential, its models and its endpoint) and from what a person or the credential store
// holds against it (Hold).
type Health struct {
	// State is ok, cooling, exhausted, needs_login or disabled.
	State string `json:"state"`
	// Until is when the block that holds the account ends.
	Until *time.Time `json:"until,omitempty"`
	// Why says what holds the account.
	Why string `json:"why,omitempty"`
	// Source is where Until came from: provider, backoff or unverified.
	Source string `json:"source,omitempty"`
	// NextCheck is when the block is next checked (see Router.Check).
	NextCheck *time.Time `json:"next_check,omitempty"`
}

// Health states.
const (
	HealthOK         = "ok"          // serving
	HealthCooling    = "cooling"     // a short block: a rate limit, a failing endpoint, a backoff
	HealthExhausted  = "exhausted"   // a usage window ran out
	HealthNeedsLogin = "needs_login" // the provider refused the sign-in
	HealthDisabled   = "disabled"    // turned off by a person
)

// Where a block's end came from (Health.Source).
const (
	// SourceProvider: the upstream named the time, or the provider's usage reading did.
	SourceProvider = "provider"
	// SourceBackoff: core chose the time, as the upstream named none.
	SourceBackoff = "backoff"
	// SourceUnverified: the upstream named a time too far off to believe (see maxReset); the
	// account is held for two check intervals and checked after one.
	SourceUnverified = "unverified"
)

// Hold is what keeps an account out of routing besides its blocks: a person turned it off, or
// the provider refused its sign-in (Why says how).
type Hold struct {
	Disabled, NeedsLogin bool
	Why                  string
}

// The reset rules: how far what an upstream says is believed, and how long blocks are checked.
var (
	// minReset: a reset the upstream names sooner than this is held this long, so sub-second
	// hints do not turn into a storm of retries.
	minReset = time.Second
	// maxReset: the longest usage window of any supported provider is 7 days; a reset further
	// off than this is not believed.
	maxReset = 8 * 24 * time.Hour
	// defaultCheckEvery is how often a block with more than this left is checked.
	defaultCheckEvery = 15 * time.Minute
	// exhaustedFrom: a quota block at least this long shows as exhausted, a shorter one as
	// cooling.
	exhaustedFrom = 5 * time.Minute
)

const unverifiedWhy = "the provider named a reset more than 8 days away, which is not believed: held until a check"

// SetCheckInterval sets how often long blocks are checked, 15 minutes unless set. An unbelieved
// reset is held for two intervals. For tests.
func (s *State) SetCheckInterval(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkEvery = d
}

// resetFor bounds a reset the upstream named, in milliseconds: shorter than minReset is
// minReset; longer than maxReset is not believed, and is held for two check intervals instead.
func (s *State) resetFor(ms int64) (time.Duration, string) {
	switch {
	case ms > maxReset.Milliseconds():
		return 2 * s.checkEvery, SourceUnverified
	case ms < minReset.Milliseconds():
		return minReset, SourceProvider
	}
	return time.Duration(ms) * time.Millisecond, SourceProvider
}

// set makes b, a block of kind on scope k, end at until. A rate limit or quota block on an
// account's credential or one of its models is checked an interval on, when it has more than
// an interval left or its end was not believed. Call with s.mu held.
func (s *State) set(b *block, k scopeKey, kind string, until time.Time, source, why string, now time.Time) {
	b.until, b.source, b.why, b.pass, b.check = until, source, why, false, time.Time{}
	b.told = kind == blockQuota && source == SourceProvider
	b.exhausted = kind == blockQuota && (source == SourceUnverified || until.Sub(now) >= exhaustedFrom)
	if kind != blockCooldown && k.kind != scopeEndpoint && (source == SourceUnverified || until.Sub(now) > s.checkEvery) {
		b.check = now.Add(s.checkEvery)
	}
}

// Health returns account a's health at now. A hold wins over every block. Otherwise the block
// that holds the account longest decides, an exhausted one before any other: a block on one of
// its models counts too, and Why then names the model.
func (s *State) Health(a Account, now time.Time, h Hold) Health {
	switch {
	case h.Disabled:
		return Health{State: HealthDisabled, Why: "turned off"}
	case h.NeedsLogin:
		return Health{State: HealthNeedsLogin, Why: cmp.Or(h.Why, "the provider refused the sign-in")}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var top *block
	var topKey scopeKey
	topKind := 0
	for k, st := range s.scopes {
		if !s.covers(k, &a) {
			continue
		}
		for i := range st.blocks {
			b := &st.blocks[i]
			if !b.until.After(now) {
				continue
			}
			if top == nil || before(b, k, i, top, topKey, topKind) {
				top, topKey, topKind = b, k, i
			}
		}
	}
	if top == nil {
		return Health{State: HealthOK}
	}
	out := Health{State: HealthCooling, Source: cmp.Or(top.source, SourceBackoff), Why: blockWhy(top, topKey, topKind)}
	if top.exhausted {
		out.State = HealthExhausted
	}
	until := top.until
	out.Until = &until
	if !top.check.IsZero() && !top.pass {
		check := top.check
		out.NextCheck = &check
	}
	return out
}

// before reports whether block b (of kind index i on k) decides an account's health over block
// c (of kind index j on l): exhausted first, then the later end, then a fixed order.
func before(b *block, k scopeKey, i int, c *block, l scopeKey, j int) bool {
	if b.exhausted != c.exhausted {
		return b.exhausted
	}
	if !b.until.Equal(c.until) {
		return b.until.After(c.until)
	}
	if k.kind != l.kind {
		return k.kind < l.kind
	}
	if k.model != l.model {
		return k.model < l.model
	}
	return i < j
}

// blockWhy says what block b, of kind index i on k, holds the account for.
func blockWhy(b *block, k scopeKey, i int) string {
	why := b.why
	if why == "" {
		switch blockKinds[i] {
		case blockQuota:
			why = "usage window used up"
		case blockRateLimit:
			why = "rate limited"
		default:
			why = "failing"
			if b.reason != nil && strings.TrimSpace(b.reason.Message) != "" {
				why = strings.TrimSpace(b.reason.Message)
			}
		}
	}
	switch k.kind {
	case scopeModel:
		return "model " + k.model + ": " + why
	case scopeEndpoint:
		return "endpoint: " + why
	}
	return why
}
