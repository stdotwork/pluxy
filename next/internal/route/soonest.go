package route

import (
	"context"
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const defaultQuotaRefresh = 30 * time.Minute

func (c *Config) QuotaRefresh() time.Duration {
	if c.QuotaRefreshMS == nil {
		return defaultQuotaRefresh
	}
	return time.Duration(*c.QuotaRefreshMS) * time.Millisecond
}

type Rank struct {
	Reset         *time.Time `json:"reset,omitempty"`
	WindowSeconds int64      `json:"window_seconds,omitempty"`
	FullUntil     *time.Time `json:"full_until,omitempty"`
}

const (
	rankKnown = iota
	rankUnknown
	rankFull
)

func (r Rank) order() (class int, key int64) {
	switch {
	case r.FullUntil != nil:
		return rankFull, r.FullUntil.Unix()
	case r.Reset != nil:
		return rankKnown, r.Reset.Unix()
	}
	return rankUnknown, 0
}

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
		t := time.Unix(longest.ResetsAt, 0).UTC().Round(time.Hour)
		r.Reset, r.WindowSeconds = &t, longest.WindowSeconds
	}
	if fullUntil > 0 {
		t := time.Unix(fullUntil, 0).UTC()
		r.FullUntil = &t
	}
	return r
}

type usageReading struct {
	limits []protocol.QuotaLimit
	at     time.Time
}

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

func (s *State) rank(a *Account, now time.Time) Rank {
	s.mu.Lock()
	u, ok := s.usage[a.ID]
	s.mu.Unlock()
	if !ok {
		return Rank{}
	}
	return rankOf(u.limits, now)
}

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
	s.mu.Lock()
	for id := range s.usage {
		if !routes[id] {
			delete(s.usage, id)
		}
	}
	s.mu.Unlock()
}
