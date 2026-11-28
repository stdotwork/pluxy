package route

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

const (
	StrategyRoundRobin   = "round-robin"
	StrategyWeighted     = "weighted"
	StrategyFillFirst    = "fill-first"
	StrategyLeastActive  = "least-active"
	StrategySoonestReset = "soonest-reset"
)

var Strategies = []string{StrategyRoundRobin, StrategyWeighted, StrategyFillFirst, StrategyLeastActive, StrategySoonestReset}

type picker struct {
	strategy string
	order    map[string]int
	gen      uint64
	*rotation
	own map[string]map[string]int64
}

type rotation struct {
	mu           sync.Mutex
	last         map[string]string
	current      map[string]map[string]int64
	used         map[string]time.Time
	lastSweep    time.Time
	weightsSince uint64
}

func newRotation() *rotation {
	return &rotation{last: map[string]string{}, current: map[string]map[string]int64{}, used: map[string]time.Time{}}
}

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

func (p *picker) setOrder(accounts []*Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.order = map[string]int{}
	for i, a := range accounts {
		p.order[a.ID] = i
	}
}

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

func (p *picker) after(model string, cands []*Account) *Account {
	pos, ok := p.order[p.last[model]]
	if !ok {
		return cands[0]
	}
	for _, c := range cands {
		if p.order[c.ID] > pos {
			return c
		}
	}
	return cands[0]
}

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
