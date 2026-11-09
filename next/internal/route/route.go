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
	"slices"
	"sync"
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
	// ForceModelPrefix: an account with a Prefix serves its models only under
	// "<prefix>/<model>", except a model whose name is the prefix itself (today's
	// force-model-prefix).
	ForceModelPrefix bool `json:"force_model_prefix,omitempty"`
}

// AffinityConfig turns on session affinity.
type AffinityConfig struct {
	// TTLMS is how long a session stays bound after its last request. Default one hour.
	TTLMS int64 `json:"ttl_ms,omitempty"`
}

// Validate checks the strategy name and limits.
func (c *Config) Validate() error {
	if _, err := newPicker(c.Strategy, nil, nil, 0); err != nil {
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

// AttemptFunc runs one attempt: a generate call with one account on extension, the provider
// extension that serves it (Account.Extension).
type AttemptFunc func(ctx context.Context, extension string, account protocol.Account, req llm.Request) Stream

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
	// Attempt, when set, runs this request's attempts in place of the router's AttemptFunc (a
	// WebSocket turn that goes to an open session). Routing, state and retries are the same.
	Attempt AttemptFunc
	// Attempting, when set, is called with the account's ID each time an account is chosen for
	// an attempt, before the attempt starts, on the goroutine that called Next.
	Attempting func(account string)
}

// Credentials supplies the credentials of managed accounts (Account.Managed): core's
// credential store, which renews them through the provider extension.
type Credentials interface {
	// Credential returns the credential an attempt on the account presents to extension, and
	// its version: only when the account belongs to extension. It renews the credential first
	// when it is about to expire, sharing the renewal with concurrent callers. An error is the
	// attempt's outcome: the account cannot be used now.
	Credential(ctx context.Context, extension, accountID string) (*protocol.Credential, uint64, *llm.Error)
	// Rejected reports that the upstream refused the credential at version. It renews the
	// credential (shared with concurrent callers) and reports whether a newer one is ready.
	Rejected(ctx context.Context, accountID string, version uint64) bool
}

// Router routes requests over a set of accounts.
type Router struct {
	cfg      Config
	attempt  AttemptFunc
	state    *State
	picker   *picker
	affinity *affinity // nil without session affinity
	ttl      time.Duration
	gen      uint64    // this router's generation, from state (see State.Retain)
	created  time.Time // when the router was made: the created time of models accounts list themselves

	mu       sync.RWMutex // guards accounts and creds
	accounts []*Account
	creds    Credentials

	// For tests: the clock, the wait, the source of jitter, and a hook run after an account
	// is chosen and before it is reserved.
	now           func() time.Time
	sleep         func(ctx context.Context, d time.Duration) error
	rand          func() float64
	beforeReserve func(account string)
}

// New returns a router for accounts. state may be shared with other routers, such as the
// router of the config a reload replaces (see State.Retain); nil starts empty.
func New(cfg Config, accounts []Account, attempt AttemptFunc, state *State) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if state == nil {
		state = NewState()
	}
	r := &Router{cfg: cfg, attempt: attempt, state: state, gen: state.nextGen(), created: time.Now(), now: time.Now, sleep: sleepCtx, rand: rand.Float64}
	var err error
	if r.accounts, err = accountList(accounts, r.gen); err != nil {
		return nil, err
	}
	if r.picker, err = newPicker(cfg.Strategy, r.accounts, state.rotation, r.gen); err != nil {
		return nil, err
	}
	if cfg.SessionAffinity != nil {
		ttl := time.Duration(cfg.SessionAffinity.TTLMS) * time.Millisecond
		if ttl == 0 {
			ttl = time.Hour
		}
		r.affinity, r.ttl = state.affinity, ttl
	}
	return r, nil
}

func accountList(accounts []Account, gen uint64) ([]*Account, error) {
	var out []*Account
	seen := map[string]bool{}
	for i := range accounts {
		a := accounts[i]
		if a.ID == "" || seen[a.ID] {
			return nil, fmt.Errorf("account IDs must be set and unique (%q)", a.ID)
		}
		if err := a.checkNames(); err != nil {
			return nil, fmt.Errorf("account %q: %w", a.ID, err)
		}
		seen[a.ID] = true
		a.gen = gen
		out = append(out, &a)
	}
	return out, nil
}

// SetAccounts replaces the accounts requests are routed over. Requests already routing keep
// the set they started with. Routing state of an account that leaves the set is dropped, so an
// account added again later under the same ID starts clean.
func (r *Router) SetAccounts(accounts []Account) error {
	list, err := accountList(accounts, r.gen)
	if err != nil {
		return err
	}
	r.mu.Lock()
	old := r.accounts
	r.accounts = list
	r.picker.setOrder(list)
	r.mu.Unlock()
	for _, a := range old {
		if !slices.ContainsFunc(list, func(b *Account) bool { return b.ID == a.ID }) {
			r.state.forget(a.ID)
		}
	}
	return nil
}

// Accounts returns the accounts requests are routed over now.
func (r *Router) Accounts() []Account {
	accounts, _ := r.snapshot()
	out := make([]Account, len(accounts))
	for i, a := range accounts {
		out[i] = *a
	}
	return out
}

// UseCredentials sets where managed accounts' credentials come from.
func (r *Router) UseCredentials(c Credentials) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creds = c
}

func (r *Router) snapshot() ([]*Account, Credentials) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.accounts, r.creds
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

	started  bool
	final    *llm.Event // set when the request ended without a running attempt
	cur      Stream     // the attempt whose reply reached the caller
	acct     *Account
	seq      uint64
	upstream map[string]string // each capable account's name for the request's model, by account ID
	ended    bool              // the attempt's reservation is released

	creds   Credentials
	renewed map[string]bool // accounts whose refused credential was renewed during this request
	fresh   bool            // the last failed attempt's credential has been renewed: try it again
	again   *Account        // the account to try next, its credential just renewed
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
			c.r.state.success(c.acct, c.upstream[c.acct.ID], c.seq)
		} else {
			c.r.state.failure(c.acct, c.upstream[c.acct.ID], ev.Error, c.r.now())
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
	accounts, creds := r.snapshot()
	c.creds = creds
	var capable []*Account
	c.upstream = map[string]string{}
	for _, a := range accounts {
		if c.req.Pin != "" && a.ID != c.req.Pin || r.picker.strategy == StrategyWeighted && a.weight() == 0 {
			continue
		}
		// Blocks on a model are kept under its upstream name, which is the account's own.
		if up, ok := a.resolve(model, r.cfg.ForceModelPrefix); ok {
			capable = append(capable, a)
			c.upstream[a.ID] = up
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
			until, reason, full := r.state.availability(a, c.upstream[a.ID], now)
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
			a := c.again
			if c.again == nil || !slices.Contains(cands, c.again) {
				a = c.choose(cands, now)
			}
			c.again = nil
			if r.beforeReserve != nil {
				r.beforeReserve(a.ID)
			}
			seq, ok := r.state.reserve(a, c.upstream[a.ID], r.now())
			if !ok {
				// Another request took the account's last slot, or a block landed, since the
				// candidates were gathered: choose again.
				continue
			}
			attempts++
			tried[a.ID], used[a.ID] = true, true
			ev, started := c.try(a, seq)
			if started {
				return ev
			}
			last = ev.Error
			if c.fresh {
				// The upstream refused the account's credential and a renewed one is ready:
				// the account may serve the request after all.
				c.fresh = false
				delete(tried, a.ID)
				c.again = a
				continue
			}
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
	return r.picker.pick(c.req.LLM.Model, best, r.state.activeCount, now)
}

func (c *call) affinityKey() affinityKey {
	return affinityKey{tenant: c.req.Tenant, model: c.req.LLM.Model, session: c.req.Session}
}

// try runs one attempt on a, reserved with sequence number seq, and reads its first event.
// started is true when the reply began; the attempt then belongs to the call.
func (c *call) try(a *Account, seq uint64) (ev llm.Event, started bool) {
	r := c.r
	if c.req.Attempting != nil {
		c.req.Attempting(a.ID)
	}
	call := a.call()
	var version uint64
	if a.Managed {
		var e *llm.Error
		if c.creds == nil {
			e = &llm.Error{Code: llm.CodeUnavailable, Scope: llm.ScopeCredential, Message: fmt.Sprintf("account %q has no credential store", a.ID)}
		} else {
			call.Credential, version, e = c.creds.Credential(c.ctx, a.Extension, a.ID)
		}
		if e != nil {
			r.state.failure(a, c.upstream[a.ID], e, r.now())
			r.state.end(a)
			return errorEvent(e), false
		}
	}
	attempt := r.attempt
	if c.req.Attempt != nil {
		attempt = c.req.Attempt
	}
	s := attempt(c.ctx, a.Extension, call, renamed(c.req.LLM, c.upstream[a.ID]))
	ev = s.Next()
	if ev.Type == llm.EventError {
		s.Close()
		if a.Managed && c.creds != nil && credentialRefused(ev.Error) && !c.renewed[a.ID] {
			// A refused credential may only need renewing (revoked early, or expired before
			// its stated time): renew it once per request and per account, and try again.
			if c.renewed == nil {
				c.renewed = map[string]bool{}
			}
			c.renewed[a.ID] = true
			if c.creds.Rejected(c.ctx, a.ID, version) {
				c.fresh = true
				r.state.end(a)
				return ev, false
			}
		}
		r.state.failure(a, c.upstream[a.ID], ev.Error, r.now())
		r.state.end(a)
		return ev, false
	}
	c.cur, c.acct, c.seq = s, a, seq
	if r.affinity != nil && c.req.Session != "" {
		r.affinity.bind(c.affinityKey(), a, r.now(), r.ttl)
	}
	if ev.Terminal() {
		// A reply that is finished in its first event: record it now.
		c.r.state.success(a, c.upstream[a.ID], seq)
		c.release()
	}
	return ev, true
}

// retrySafe reports whether a failed attempt may be tried again elsewhere: the extension says
// the upstream certainly did not run it, and the failure is about the account (so the account
// is now blocked and the next attempt goes to another one, or waits). An account that cannot
// serve this request (unsupported, about the account rather than the request) is not blocked:
// it was tried this round, so the next attempt goes to another account.
func retrySafe(e *llm.Error) bool {
	if e == nil || !e.Retryable {
		return false
	}
	if e.Code == llm.CodeUnsupported {
		return e.Scope != "" && e.Scope != llm.ScopeRequest
	}
	kind, _, _, _, _ := classify(e)
	return kind != ""
}

// credentialRefused reports whether the upstream refused the account's credential itself.
func credentialRefused(e *llm.Error) bool {
	return e != nil && e.Code == llm.CodeAuth && (e.Scope == "" || e.Scope == llm.ScopeCredential)
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
