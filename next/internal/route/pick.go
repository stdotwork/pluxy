package route

import (
	"fmt"
	"slices"
	"sync"
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
)

// Strategies lists the valid strategy names.
var Strategies = []string{StrategyRoundRobin, StrategyWeighted, StrategyFillFirst, StrategyLeastActive}

// picker chooses among eligible accounts. Its state is kept per model, so rotation for one
// model does not disturb another.
type picker struct {
	strategy string
	order    map[string]int // account ID -> position in config

	mu      sync.Mutex
	last    map[string]string           // model -> account ID picked last
	current map[string]map[string]int64 // model -> account ID -> smooth weighted current weight
}

func newPicker(strategy string, accounts []*Account) (*picker, error) {
	if strategy == "" {
		strategy = StrategyRoundRobin
	}
	if !slices.Contains(Strategies, strategy) {
		return nil, fmt.Errorf("unknown routing strategy %q (want one of %v)", strategy, Strategies)
	}
	p := &picker{strategy: strategy, order: map[string]int{}, last: map[string]string{}, current: map[string]map[string]int64{}}
	for i, a := range accounts {
		p.order[a.ID] = i
	}
	return p, nil
}

// pick returns one of cands, which are in configured order and not empty. active reports the
// running attempts on an account.
func (p *picker) pick(model string, cands []*Account, active func(string) int) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	default:
		a = p.after(model, cands)
	}
	p.last[model] = a.ID
	return a
}

// after returns the first candidate that comes after the account picked last for model, in
// configured order, wrapping around.
func (p *picker) after(model string, cands []*Account) *Account {
	last, ok := p.last[model]
	if !ok {
		return cands[0]
	}
	pos := p.order[last]
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
	cur := p.current[model]
	if cur == nil {
		cur = map[string]int64{}
		p.current[model] = cur
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
