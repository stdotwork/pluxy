package route

import (
	"cmp"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

type Health struct {
	State     string     `json:"state"`
	Until     *time.Time `json:"until,omitempty"`
	Why       string     `json:"why,omitempty"`
	Source    string     `json:"source,omitempty"`
	NextCheck *time.Time `json:"next_check,omitempty"`
}

const (
	HealthOK         = "ok"
	HealthCooling    = "cooling"
	HealthExhausted  = "exhausted"
	HealthNeedsLogin = "needs_login"
	HealthBanned     = "banned"
	HealthDisabled   = "disabled"
	HealthBroken     = "broken"
)

const (
	SourceProvider   = "provider"
	SourceBackoff    = "backoff"
	SourceUnverified = "unverified"
)

type Hold struct {
	Disabled, NeedsLogin bool
	Why                  string
}

var (
	minReset = time.Second
	maxReset = 8 * 24 * time.Hour
	// Codex's monthly window, 28 to 31 days.
	maxWindow = 31 * 24 * time.Hour
	// Providers name resets as Unix seconds, set against their own clock and truncated to the
	// second, and a window's length is its nominal one.
	resetSlack        = time.Hour
	defaultCheckEvery = 15 * time.Minute
	exhaustedFrom     = 5 * time.Minute
)

const unverifiedWhy = "the provider named a reset too far off to believe: held until a check"

func (s *State) SetCheckInterval(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkEvery = d
}

func (s *State) resetFor(ms, windowMin int64) (time.Duration, string) {
	limit := maxReset
	if windowMin > 0 && windowMin <= int64(maxWindow/time.Minute) {
		limit = max(limit, time.Duration(windowMin)*time.Minute+resetSlack)
	}
	switch {
	case ms > limit.Milliseconds():
		return 2 * s.checkEvery, SourceUnverified
	case ms < minReset.Milliseconds():
		return minReset, SourceProvider
	}
	return time.Duration(ms) * time.Millisecond, SourceProvider
}

func (s *State) set(b *block, k scopeKey, kind string, until time.Time, source, why string, now time.Time) {
	b.until, b.source, b.why, b.pass, b.check = until, source, why, false, time.Time{}
	b.told = kind == blockQuota && source == SourceProvider
	b.exhausted = kind == blockQuota && (source == SourceUnverified || until.Sub(now) >= exhaustedFrom)
	if kind != blockCooldown && k.kind != scopeEndpoint && (source == SourceUnverified || until.Sub(now) > s.checkEvery) {
		b.check = now.Add(s.checkEvery)
	}
}

func (s *State) Health(a Account, now time.Time, h Hold) Health {
	if h.Disabled {
		return Health{State: HealthDisabled, Why: "turned off"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ban, ok := s.bans[a.ID]; ok {
		return Health{State: HealthBanned, Why: cmp.Or(ban.why, "the provider closed or suspended the account"), Source: SourceProvider}
	}
	if h.NeedsLogin {
		return Health{State: HealthNeedsLogin, Why: cmp.Or(h.Why, "the provider refused the sign-in")}
	}
	var top *block
	var topKey scopeKey
	topKind := 0
	for k, st := range s.scopes {
		if !s.covers(k, &a) {
			continue
		}
		for i := range st.blocks {
			b := &st.blocks[i]
			if !b.until.After(now) || i == kindIndex(blockCooldown) && b.reason != nil && b.reason.Code == llm.CodeNotFound {
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
