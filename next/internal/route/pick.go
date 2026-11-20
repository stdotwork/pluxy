package route

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// Strategies: how one account is chosen from the eligible ones.
const (
	// StrategyRoundRobin takes turns. The turn follows the account picked last, not a numeric
	// cursor, so a candidate list that shrinks during retries does not skip or starve anyone.
	StrategyRoundRobin = "round-robin"
	// StrategyWeighted takes turns in proportion to each account's weight, interleaved
	// (smooth weighted round robin, as in nginx).
	StrategyWeighted = "weighted"
	// StrategyFillFirst uses the first eligible account in configured order until it is
	// blocked.
	StrategyFillFirst = "fill-first"
	// StrategyLeastActive picks the account with the fewest attempts running, taking turns
	// among ties. It scans every candidate, which suits pools of a few dozen accounts.
	StrategyLeastActive = "least-active"
	// StrategySoonestReset picks the account whose longest usage window resets soonest, by the
	// hour, so allowance about to reset is spent before it is lost; accounts with no known reset
	// come after, and those with a full window last. Ties take turns. (See Rank.)
	StrategySoonestReset = "soonest-reset"
)

// Strategies lists the valid strategy names.
var Strategies = []string{StrategyRoundRobin, StrategyWeighted, StrategyFillFirst, StrategyLeastActive, StrategySoonestReset}

// picker chooses among eligible accounts. Its memory is kept per model, so rotation for one
// model does not disturb another, and lives in State, so rotation carries on across a reload.
type picker struct {
	strategy string
	order    map[string]int // account ID -> position in config
	gen      uint64         // the router's generation
	*rotation
	// own holds the weighted balances once the weights in State have moved on to a newer
	// router's (rotation.weightsSince): this picker's old weights must not disturb them.
	own map[string]map[string]int64
}

// rotation is a picker's memory.
type rotation struct {
	mu        sync.Mutex
	last      map[string]string           // model -> account ID picked last
	current   map[string]map[string]int64 // model -> account ID -> smooth weighted current weight
	used      map[string]time.Time        // model -> when its cursor was last used
	lastSweep time.Time
	// weightsSince is the generation of the router whose weights current is for; older
	// routers keep their own balances.
	weightsSince uint64
}

func newRotation() *rotation {
	return &rotation{last: map[string]string{}, current: map[string]map[string]int64{}, used: map[string]time.Time{}}
}

// newPicker returns a picker for accounts on the router of generation gen; mem is its memory,
// nil for a fresh one.
func newPicker(strategy string, accounts []*Account, mem *rotation, gen uint64) (*picker, error) {
	if strategy == "" {
		strategy = StrategyRoundRobin
	}
	if !slices.Contains(Strategies, strategy) {
		return nil, fmt.Errorf("unknown routing strategy %q (want one of %v)", strategy, Strategies)
	}
	if mem == nil {
		mem = newRotation()
	}
	p := &picker{strategy: strategy, order: map[string]int{}, gen: gen, rotation: mem}
	for i, a := range accounts {
		p.order[a.ID] = i
	}
	return p, nil
}

// setOrder replaces the configured order with that of accounts.
func (p *picker) setOrder(accounts []*Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.order = map[string]int{}
	for i, a := range accounts {
		p.order[a.ID] = i
	}
}

// forgetModels drops rotation cursors unused for stateIdle, and the least recently used ones
// beyond maxModelKeys. Losing a cursor only restarts that model's rotation. Call with p.mu
// held.
func (p *picker) forgetModels(now time.Time) {
	if now.Sub(p.lastSweep) >= sweepEvery {
		p.lastSweep = now
		for m, t := range p.used {
			if now.Sub(t) > stateIdle {
				p.forget(m)
			}
		}
	}
	for len(p.used) > maxModelKeys {
		oldest, first := "", true
		var at time.Time
		for m, t := range p.used {
			if first || t.Before(at) {
				oldest, at, first = m, t, false
			}
		}
		p.forget(oldest)
	}
}

func (p *picker) forget(model string) {
	delete(p.used, model)
	delete(p.last, model)
	delete(p.current, model)
	delete(p.own, model)
}

// pick returns one of cands, which are in configured order and not empty. active reports the
// running attempts on an account, and rank what soonest-reset ranks it by.
func (p *picker) pick(model string, cands []*Account, active func(string) int, rank func(*Account, time.Time) Rank, now time.Time) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used[model] = now
	defer p.forgetModels(now)
	var a *Account
	switch p.strategy {
	case StrategyFillFirst:
		a = cands[0]
	case StrategyWeighted:
		a = p.weighted(model, cands)
	case StrategyLeastActive:
		least, fewest := []*Account(nil), -1
		for _, c := range cands {
			n := active(c.ID)
			switch {
			case fewest < 0 || n < fewest:
				least, fewest = []*Account{c}, n
			case n == fewest:
				least = append(least, c)
			}
		}
		a = p.after(model, least)
	case StrategySoonestReset:
		a = p.after(model, soonest(cands, rank, now))
	default:
		a = p.after(model, cands)
	}
	p.last[model] = a.ID
	return a
}

// preview returns a picker over a copy of model's memory: picks on it leave this picker's
// untouched, so a plan can ask what the next picks would be.
func (p *picker) preview(model string) *picker {
	p.mu.Lock()
	defer p.mu.Unlock()
	mem := newRotation()
	if last, ok := p.last[model]; ok {
		mem.last[model] = last
	}
	balances := p.current
	if p.gen < p.weightsSince {
		balances = p.own
	}
	if cur := balances[model]; cur != nil {
		mem.current[model] = maps.Clone(cur)
	}
	return &picker{strategy: p.strategy, order: maps.Clone(p.order), rotation: mem}
}

// after returns the first candidate that comes after the account picked last for model, in
// configured order, wrapping around.
func (p *picker) after(model string, cands []*Account) *Account {
	pos, ok := p.order[p.last[model]]
	if !ok { // none yet, or one a reload removed
		return cands[0]
	}
	for _, c := range cands {
		if p.order[c.ID] > pos {
			return c
		}
	}
	return cands[0]
}

// weighted is smooth weighted round robin: every candidate gains its weight, the highest
// total wins and pays back the sum of the weights. Candidates all have a weight above 0: the
// router leaves weight-0 accounts out of weighted routing.
func (p *picker) weighted(model string, cands []*Account) *Account {
	balances := p.current
	if p.gen < p.weightsSince {
		if p.own == nil {
			p.own = map[string]map[string]int64{}
		}
		balances = p.own
	}
	cur := balances[model]
	if cur == nil {
		cur = map[string]int64{}
		balances[model] = cur
	}
	var best *Account
	var total int64
	for _, c := range cands {
		w := int64(c.weight())
		total += w
		cur[c.ID] += w
		if best == nil || cur[c.ID] > cur[best.ID] {
			best = c
		}
	}
	cur[best.ID] -= total
	return best
}
