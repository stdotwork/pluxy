package route

import (
	"context"
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type Readings interface {
	Reading(ctx context.Context, a Account) (*protocol.Quota, *llm.Error)
}

func (r *Router) UseReadings(rd Readings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readings = rd
}

var noReading = &llm.Error{Code: llm.CodeUnsupported, Message: "no usage reading"}

func (r *Router) Check(ctx context.Context) {
	accounts, _ := r.snapshot()
	r.mu.RLock()
	rd := r.readings
	r.mu.RUnlock()
	routes := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		routes[a.ID] = true
	}
	due := r.state.due(r.now(), routes)
	for _, a := range accounts {
		claims := due[a.ID]
		if len(claims) == 0 {
			continue
		}
		q, e := (*protocol.Quota)(nil), noReading
		if rd != nil {
			q, e = rd.Reading(ctx, *a)
		}
		r.state.checked(claims, q, e, r.now())
		r.state.noteReading(a.ID, q, e, r.now())
	}
}

func (s *State) nextCheck(until, now time.Time) time.Time {
	if until.Sub(now) > s.checkEvery {
		return now.Add(s.checkEvery)
	}
	return time.Time{}
}

type claim struct {
	key   scopeKey
	i     int
	mark  uint64
	until time.Time
}

func (s *State) due(now time.Time, routes map[string]bool) map[string][]claim {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]claim{}
	for k, st := range s.scopes {
		if k.kind == scopeEndpoint || !routes[k.id] {
			continue
		}
		if _, banned := s.bans[k.id]; banned {
			continue
		}
		for i := range st.blocks {
			b := &st.blocks[i]
			if b.check.IsZero() || b.check.After(now) || b.pass || !b.until.After(now) {
				continue
			}
			b.check = s.nextCheck(b.until, now)
			out[k.id] = append(out[k.id], claim{key: k, i: i, mark: b.mark, until: b.until})
		}
	}
	return out
}

func (s *State) checked(claims []claim, q *protocol.Quota, e *llm.Error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range claims {
		st := s.scopes[c.key]
		if st == nil {
			continue
		}
		b := &st.blocks[c.i]
		if b.mark != c.mark || !b.until.Equal(c.until) || !b.until.After(now) {
			continue
		}
		switch {
		case e != nil && e.Code == llm.CodeUnsupported:
			b.pass, b.check = true, time.Time{}
		case e != nil || q == nil:
		default:
			reset, window, full := fullWindow(q, c.key, now)
			if !full {
				*b = block{mark: b.mark, reason: b.reason}
				break
			}
			d, source := s.resetFor(reset.Sub(now).Milliseconds(), window)
			why := ""
			if source == SourceUnverified {
				why = unverifiedWhy
			}
			s.set(b, c.key, blockKinds[c.i], now.Add(d), source, why, now)
			b.window = window
		}
		s.notify()
	}
}

func fullWindow(q *protocol.Quota, k scopeKey, now time.Time) (reset time.Time, window int64, full bool) {
	for _, l := range q.Limits {
		if l.Feature || l.Unlimited || l.ResetsAt <= now.Unix() {
			continue
		}
		if len(l.Models) > 0 && (k.kind != scopeModel || !slices.Contains(l.Models, k.model)) {
			continue
		}
		if !(l.UsedPercent != nil && *l.UsedPercent >= 100 || l.Remaining != nil && *l.Remaining <= 0) {
			continue
		}
		if t := time.Unix(l.ResetsAt, 0); t.After(reset) {
			reset, window, full = t, l.WindowSeconds/60, true
		}
	}
	return reset, window, full
}
