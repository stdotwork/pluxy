package route

import (
	"context"
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The soonest-reset strategy spends allowance that is about to reset before it is lost: among the
// eligible accounts of the top priority it picks the one whose longest usage window resets
// soonest. It ranks accounts by the usage readings Refresh and Check take (the extension's quota
// call), never by a request's outcome.

// defaultQuotaRefresh is how often Refresh reads the accounts' usage when
// routing.quota_refresh_ms is not set.
const defaultQuotaRefresh = 30 * time.Minute

// QuotaRefresh is how often soonest-reset reads the accounts' usage: routing.quota_refresh_ms,
// 30 minutes when unset; 0 turns the readings off.
func (c *Config) QuotaRefresh() time.Duration {
	if c.QuotaRefreshMS == nil {
		return defaultQuotaRefresh
	}
	return time.Duration(*c.QuotaRefreshMS) * time.Millisecond
}

// Rank is what soonest-reset ranked an account by: the reset of its longest usage window, or
// that its allowance is used up until a reset, or neither when no reading says.
type Rank struct {
	// Reset is when the account's longest usage window resets, rounded to the hour; nil when no
	// reading names one.
	Reset *time.Time `json:"reset,omitempty"`
	// WindowSeconds is that window's length, when the provider gave one.
	WindowSeconds int64 `json:"window_seconds,omitempty"`
	// FullUntil, when set, is when the latest of the account's full usage windows resets: the
	// account has no allowance left until then, so it ranks after every other.
	FullUntil *time.Time `json:"full_until,omitempty"`
}

// Rank classes, in the order soonest-reset tries them.
const (
	rankKnown   = iota // a reset is known and allowance is left
	rankUnknown        // no reading names a reset
	rankFull           // a usage window is full
)

// order returns r's class and its key within the class: the earlier, the sooner it is picked.
func (r Rank) order() (class int, key int64) {
	switch {
	case r.FullUntil != nil:
		return rankFull, r.FullUntil.Unix()
	case r.Reset != nil:
		return rankKnown, r.Reset.Unix()
	}
	return rankUnknown, 0
}

// rankOf ranks the usage windows in limits at now. Only windows that count the whole account
// take part: one that names models or counts a feature of the plan says nothing about the
// account's own allowance, and a balance or count without a reset time has no reset to rank by.
// The longest window is the one with the largest length; among windows of one length, or with
// none given, the one that resets last.
func rankOf(limits []protocol.QuotaLimit, now time.Time) Rank {
	var r Rank
	var longest *protocol.QuotaLimit
	var fullUntil int64
	for i := range limits {
		l := &limits[i]
		if l.Feature || l.Unlimited || len(l.Models) > 0 || l.ResetsAt <= now.Unix() {
			continue
		}
		if l.UsedPercent != nil && *l.UsedPercent >= 100 || l.Remaining != nil && *l.Remaining <= 0 {
			fullUntil = max(fullUntil, l.ResetsAt)
		}
		if longest == nil || l.WindowSeconds > longest.WindowSeconds ||
			l.WindowSeconds == longest.WindowSeconds && l.ResetsAt > longest.ResetsAt {
			longest = l
		}
	}
	if longest != nil {
		// Rounded to the hour, so accounts whose windows reset minutes apart tie and take turns,
		// and the order holds still between readings.
		t := time.Unix(longest.ResetsAt, 0).UTC().Round(time.Hour)
		r.Reset, r.WindowSeconds = &t, longest.WindowSeconds
	}
	if fullUntil > 0 {
		t := time.Unix(fullUntil, 0).UTC()
		r.FullUntil = &t
	}
	return r
}

// usageReading is the latest usage reading of an account.
type usageReading struct {
	limits []protocol.QuotaLimit
	at     time.Time
}

// noteReading keeps the outcome of a usage reading of account id: a reading replaces the one
// before; an account with no reading (unsupported) forgets it; a reading that could not be made
// keeps the one before, which ranks by its own reset times until they pass.
func (s *State) noteReading(id string, q *protocol.Quota, e *llm.Error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e != nil && e.Code == llm.CodeUnsupported:
		delete(s.usage, id)
	case e != nil || q == nil:
	default:
		s.usage[id] = usageReading{limits: slices.Clone(q.Limits), at: now}
	}
}

// rank ranks account a by its latest usage reading at now.
func (s *State) rank(a *Account, now time.Time) Rank {
	s.mu.Lock()
	u, ok := s.usage[a.ID]
	s.mu.Unlock()
	if !ok {
		return Rank{}
	}
	return rankOf(u.limits, now)
}

// soonest returns the candidates of cands that rank first, in configured order.
func soonest(cands []*Account, rank func(*Account, time.Time) Rank, now time.Time) []*Account {
	var best []*Account
	bestClass, bestKey := 0, int64(0)
	for _, c := range cands {
		class, key := rank(c, now).order()
		switch {
		case best == nil || class < bestClass || class == bestClass && key < bestKey:
			best, bestClass, bestKey = []*Account{c}, class, key
		case class == bestClass && key == bestKey:
			best = append(best, c)
		}
	}
	return best
}

// Refresh reads the usage of the accounts the router routes over, when the strategy is
// soonest-reset and routing.quota_refresh_ms (QuotaRefresh) has passed since the last refresh
// began, over every router made on this state. Readings are made one at a time; an account
// whose extension has no reading, or that is banned, is left unread. Core calls Refresh on a
// timer, never on a request's path.
func (r *Router) Refresh(ctx context.Context) {
	every := r.cfg.QuotaRefresh()
	if r.picker.strategy != StrategySoonestReset || every <= 0 {
		return
	}
	now := r.now()
	s := r.state
	s.mu.Lock()
	if !s.refreshed.IsZero() && now.Sub(s.refreshed) < every {
		s.mu.Unlock()
		return
	}
	s.refreshed = now
	s.mu.Unlock()
	accounts, _ := r.snapshot()
	r.mu.RLock()
	rd := r.readings
	r.mu.RUnlock()
	if rd == nil {
		return
	}
	routes := map[string]bool{}
	for _, a := range accounts {
		routes[a.ID] = true
		if s.IsBanned(a.ID) || ctx.Err() != nil {
			continue
		}
		q, e := rd.Reading(ctx, *a)
		s.noteReading(a.ID, q, e, r.now())
	}
	// Readings of accounts that no longer route here are dropped.
	s.mu.Lock()
	for id := range s.usage {
		if !routes[id] {
			delete(s.usage, id)
		}
	}
	s.mu.Unlock()
}
