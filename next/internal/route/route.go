// Package route is core's request coordinator: for each request it chooses an account, runs
// attempts on the provider extension, reads each attempt's typed outcome and decides whether to
// retry, rotate to another account or fail. It owns the whole retry budget; extensions never
// retry on their own. Once a reply has started, nothing is retried.
//
// The steps for one request:
//
//  1. Filter: accounts that serve the model (and the pinned account, if the request pins one),
//     that no block covers (rate limit, quota or cooldown, on the account's credential, the
//     model on that account, or its endpoint), that are under their concurrency limit and that
//     this request has not tried in the current round.
//  2. Prefer: the account the request's session is bound to, if it is still eligible; else the
//     eligible accounts with the highest priority.
//  3. Pick by strategy: round robin, weighted, fill-first or least-active.
//  4. Attempt, and read the first event. An error before the reply starts is an outcome: it
//     updates the account's state, then the request rotates to another eligible account if the
//     error is retry-safe, waits for a blocked one if the wait fits the budget, or fails.
package route

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Config is the routing section of core's config. The zero value is valid.
type Config struct {
	// Strategy picks among eligible accounts: round-robin (default), weighted, fill-first or
	// least-active.
	Strategy string `json:"strategy,omitempty"`
	// MaxAttempts bounds upstream attempts per request, counting the first. 0 means no fixed
	// limit: each account is still tried at most once per round, and a new round needs a
	// wait that fits MaxWaitMS.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// MaxAccounts bounds how many distinct accounts one request tries. 0 means no limit.
	MaxAccounts int `json:"max_accounts,omitempty"`
	// MaxWaitMS is the longest one request waits, in total, for a blocked account to become
	// eligible again. 0 (the default) means it never waits: when every account is blocked it
	// fails at once, with the time the first block ends. A request never tries a blocked
	// scope early to fit this budget.
	MaxWaitMS int64 `json:"max_wait_ms,omitempty"`
	// SessionAffinity, when set, sends requests of the same session to the same account.
	SessionAffinity *AffinityConfig `json:"session_affinity,omitempty"`
}

// AffinityConfig turns on session affinity.
type AffinityConfig struct {
	// TTLMS is how long a session stays bound after its last request. Default one hour.
	TTLMS int64 `json:"ttl_ms,omitempty"`
}

// Validate checks the strategy name and limits.
func (c *Config) Validate() error {
	if _, err := newPicker(c.Strategy, nil); err != nil {
		return err
	}
	if c.MaxAttempts < 0 || c.MaxAccounts < 0 || c.MaxWaitMS < 0 {
		return fmt.Errorf("routing limits must not be negative")
	}
	if c.SessionAffinity != nil && c.SessionAffinity.TTLMS < 0 {
		return fmt.Errorf("routing.session_affinity.ttl_ms must not be negative")
	}
	return nil
}

// Stream is the reply to a request: events until one terminal event.
type Stream interface {
	Next() llm.Event
	Close()
}

// AttemptFunc runs one attempt: a generate call on the provider extension with one account.
type AttemptFunc func(ctx context.Context, account protocol.Account, req llm.Request) Stream

// Request is one client request to route.
type Request struct {
	// LLM is the request as every attempt receives it. Each attempt gets the same original
	// request, and the extension translates it for its account afresh.
	LLM llm.Request
	// Tenant identifies the caller (for example its client key) and scopes session bindings.
	Tenant string
	// Session is the client's session or conversation key, if it sent one.
	Session string
	// Pin, when set, restricts the request to the account with this ID.
	Pin string
}

// Router routes requests over a fixed set of accounts.
type Router struct {
	cfg      Config
	accounts []*Account
	attempt  AttemptFunc
	state    *State
	picker   *picker
	affinity *affinity

	// For tests: the clock, the wait, and the source of jitter.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	rand  func() float64
}

// New returns a router for accounts. state may be shared with other routers; nil starts empty.
func New(cfg Config, accounts []Account, attempt AttemptFunc, state *State) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if state == nil {
		state = NewState()
	}
	r := &Router{cfg: cfg, attempt: attempt, state: state, now: time.Now, sleep: sleepCtx, rand: rand.Float64}
	seen := map[string]bool{}
	for i := range accounts {
		a := accounts[i]
		if a.ID == "" || seen[a.ID] {
			return nil, fmt.Errorf("account IDs must be set and unique (%q)", a.ID)
		}
		seen[a.ID] = true
		r.accounts = append(r.accounts, &a)
	}
	var err error
	if r.picker, err = newPicker(cfg.Strategy, r.accounts); err != nil {
		return nil, err
	}
	if cfg.SessionAffinity != nil {
		ttl := time.Duration(cfg.SessionAffinity.TTLMS) * time.Millisecond
		if ttl == 0 {
			ttl = time.Hour
		}
		r.affinity = newAffinity(ttl)
	}
	return r, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Generate routes req. Nothing runs until the first call to Next.
func (r *Router) Generate(ctx context.Context, req Request) Stream {
	return &call{r: r, ctx: ctx, req: req}
}

// call is one routed request.
type call struct {
	r   *Router
	ctx context.Context
	req Request

	started bool
	final   *llm.Event // set when the request ended without a running attempt
	cur     Stream     // the attempt whose reply reached the caller
	acct    *Account
	seq     uint64
	ended   bool // the attempt's reservation is released
}

func (c *call) Next() llm.Event {
	if !c.started {
		c.started = true
		ev := c.route()
		if c.cur == nil {
			c.final = &ev
		}
		return ev
	}
	if c.final != nil {
		return *c.final
	}
	ev := c.cur.Next()
	if ev.Terminal() && !c.ended {
		// After the reply has started, an outcome still updates the account's state, but the
		// request is never retried: the caller may already have sent bytes to the client.
		if ev.Type == llm.EventFinish {
			c.r.state.success(c.acct, c.req.LLM.Model, c.seq)
		} else {
			c.r.state.failure(c.acct, c.req.LLM.Model, ev.Error, c.r.now())
		}
		c.release()
	}
	return ev
}

func (c *call) Close() {
	if c.cur != nil {
		c.cur.Close()
		c.release()
	}
}

func (c *call) release() {
	if !c.ended {
		c.ended = true
		c.r.state.end(c.acct)
	}
}

// route runs attempts until one starts a reply, and returns that reply's first event; or it
// gives up and returns the error event that ends the request.
func (c *call) route() llm.Event {
	r, model := c.r, c.req.LLM.Model
	var capable []*Account
	for _, a := range r.accounts {
		if a.serves(model) && (c.req.Pin == "" || a.ID == c.req.Pin) && (r.picker.strategy != StrategyWeighted || a.weight() > 0) {
			capable = append(capable, a)
		}
	}
	if len(capable) == 0 {
		msg := fmt.Sprintf("no account serves model %q", model)
		if c.req.Pin != "" {
			msg = fmt.Sprintf("account %q does not exist or does not serve model %q", c.req.Pin, model)
		}
		return errorEvent(&llm.Error{Code: llm.CodeNotFound, Message: msg, ProviderCode: "model_not_found"})
	}

	tried := map[string]bool{} // this round
	used := map[string]bool{}  // this request
	attempts := 0
	budget := time.Duration(r.cfg.MaxWaitMS) * time.Millisecond
	var last *llm.Error
	for {
		now := r.now()
		var cands []*Account
		var next time.Time // earliest time an account is eligible again, ignoring this round
		var nextReason *llm.Error
		busy := false
		for _, a := range capable {
			if r.cfg.MaxAccounts > 0 && len(used) >= r.cfg.MaxAccounts && !used[a.ID] {
				continue
			}
			until, reason, full := r.state.availability(a, model, now)
			switch {
			case full:
				busy = true
			case !until.IsZero():
				if next.IsZero() || until.Before(next) {
					next, nextReason = until, reason
				}
			case tried[a.ID]:
				next, nextReason = now, nil
			default:
				cands = append(cands, a)
			}
		}
		if r.cfg.MaxAttempts > 0 && attempts >= r.cfg.MaxAttempts {
			return errorEvent(last)
		}
		if len(cands) > 0 {
			a := c.choose(cands, now)
			attempts++
			tried[a.ID], used[a.ID] = true, true
			ev, started := c.try(a)
			if started {
				return ev
			}
			last = ev.Error
			if !retrySafe(last) {
				return ev
			}
			continue
		}
		// Nothing is eligible now: wait for the first account to come back, if that fits.
		if next.IsZero() {
			if last != nil {
				return errorEvent(last)
			}
			if busy {
				return errorEvent(&llm.Error{Code: llm.CodeRateLimit, Status: 429, ProviderCode: "accounts_busy",
					Message: fmt.Sprintf("every account for model %q is at its concurrency limit", model)})
			}
			return errorEvent(&llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("no account can serve model %q", model)})
		}
		wait := next.Sub(now)
		if wait <= 0 && last != nil {
			// Every eligible account was tried this round and none is blocked: trying them
			// again at once could loop. A new round starts only after a wait.
			return errorEvent(last)
		}
		if wait > budget {
			if last != nil {
				return errorEvent(last)
			}
			return errorEvent(coolingError(model, wait, nextReason))
		}
		// Jitter (up to a quarter of the wait, at most 2s, within the budget) so requests that
		// waited for the same account do not all arrive at once. Never shorter than the wait.
		jitter := min(time.Duration(r.rand()*float64(wait/4)), 2*time.Second, budget-wait)
		wait += jitter
		if err := r.sleep(c.ctx, wait); err != nil {
			return errorEvent(&llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"})
		}
		budget -= wait
		clear(tried)
	}
}

// choose picks one of cands: the session's account if it is among them, otherwise by priority
// and then strategy.
func (c *call) choose(cands []*Account, now time.Time) *Account {
	r := c.r
	if r.affinity != nil && c.req.Session != "" {
		if id, ok := r.affinity.lookup(c.affinityKey(), now); ok {
			for _, a := range cands {
				if a.ID == id {
					return a
				}
			}
		}
	}
	top := cands[0].Priority
	for _, a := range cands {
		top = max(top, a.Priority)
	}
	best := cands[:0:0]
	for _, a := range cands {
		if a.Priority == top {
			best = append(best, a)
		}
	}
	return r.picker.pick(c.req.LLM.Model, best, r.state.activeCount)
}

func (c *call) affinityKey() affinityKey {
	return affinityKey{tenant: c.req.Tenant, model: c.req.LLM.Model, session: c.req.Session}
}

// try runs one attempt on a and reads its first event. started is true when the reply began;
// the attempt then belongs to the call.
func (c *call) try(a *Account) (ev llm.Event, started bool) {
	r := c.r
	seq := r.state.begin(a)
	s := r.attempt(c.ctx, a.call(), c.req.LLM)
	ev = s.Next()
	if ev.Type == llm.EventError {
		s.Close()
		r.state.failure(a, c.req.LLM.Model, ev.Error, r.now())
		r.state.end(a)
		return ev, false
	}
	c.cur, c.acct, c.seq = s, a, seq
	if r.affinity != nil && c.req.Session != "" {
		r.affinity.bind(c.affinityKey(), a.ID, r.now())
	}
	if ev.Terminal() {
		// A reply that is finished in its first event: record it now.
		c.r.state.success(a, c.req.LLM.Model, seq)
		c.release()
	}
	return ev, true
}

// retrySafe reports whether a failed attempt may be tried again elsewhere: the extension says
// the upstream certainly did not run it, and the failure is about the account (so the account
// is now blocked and the next attempt goes to another one, or waits).
func retrySafe(e *llm.Error) bool {
	if e == nil || !e.Retryable {
		return false
	}
	kind, _, _, _, _ := classify(e)
	return kind != ""
}

func coolingError(model string, wait time.Duration, reason *llm.Error) *llm.Error {
	e := &llm.Error{
		Code:         llm.CodeRateLimit,
		Status:       429,
		ProviderCode: "model_cooldown",
		Message:      fmt.Sprintf("every account for model %q is cooling down; try again in %s", model, wait.Round(time.Second)),
		RetryAfterMS: (wait + time.Millisecond - 1).Milliseconds(),
	}
	if reason != nil && reason.Code == llm.CodeQuota {
		e.Code = llm.CodeQuota
	}
	return e
}

func errorEvent(e *llm.Error) llm.Event {
	return llm.Event{Type: llm.EventError, Error: e}
}
