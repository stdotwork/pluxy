package route

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/ignite"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type Config struct {
	Strategy                 string             `json:"strategy,omitempty"`
	MaxAttempts              int                `json:"max_attempts,omitempty"`
	MaxAccounts              int                `json:"max_accounts,omitempty"`
	MaxWaitMS                int64              `json:"max_wait_ms,omitempty"`
	RequestRetry             *int               `json:"request_retry,omitempty"`
	DisableCooling           bool               `json:"disable_cooling,omitempty"`
	TransientErrorCooldownMS int64              `json:"transient_error_cooldown_ms,omitempty"`
	SessionAffinity          *AffinityConfig    `json:"session_affinity,omitempty"`
	ForceModelPrefix         bool               `json:"force_model_prefix,omitempty"`
	QuotaResponseMode        string             `json:"quota_response_mode,omitempty"`
	Explain                  *bool              `json:"explain,omitempty"`
	Timeouts                 *protocol.Timeouts `json:"timeouts,omitempty"`
	Chains                   map[string][]Step  `json:"chains,omitempty"`
	QuotaRefreshMS           *int64             `json:"quota_refresh_ms,omitempty"`
	Ignition                 *ignite.Config     `json:"ignition,omitempty"`
}

const (
	QuotaRetryable  = "retryable"
	QuotaUsageLimit = "usage-limit"
)

const usageLimitMinReset = 5 * time.Minute

type AffinityConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
	TTLMS   int64 `json:"ttl_ms,omitempty"`
}

const defaultAffinityTTL = time.Hour

func (c *Config) AffinityOn() bool {
	return c.SessionAffinity == nil || c.SessionAffinity.Enabled == nil || *c.SessionAffinity.Enabled
}

func (c *Config) AffinityTTL() time.Duration {
	if c.SessionAffinity == nil || c.SessionAffinity.TTLMS == 0 {
		return defaultAffinityTTL
	}
	return time.Duration(c.SessionAffinity.TTLMS) * time.Millisecond
}

func (c *Config) Validate() error {
	if _, err := newPicker(c.Strategy, nil, nil, 0); err != nil {
		return err
	}
	if c.MaxAttempts < 0 || c.MaxAccounts < 0 || c.MaxWaitMS < 0 || c.RequestRetry != nil && *c.RequestRetry < 0 {
		return fmt.Errorf("routing limits must not be negative")
	}
	if c.QuotaRefreshMS != nil && *c.QuotaRefreshMS < 0 {
		return fmt.Errorf("routing.quota_refresh_ms must not be negative")
	}
	if c.SessionAffinity != nil && c.SessionAffinity.TTLMS < 0 {
		return fmt.Errorf("routing.session_affinity.ttl_ms must not be negative")
	}
	switch c.QuotaResponseMode {
	case "", QuotaRetryable, QuotaUsageLimit:
	default:
		return fmt.Errorf("routing.quota_response_mode %q: want %q or %q", c.QuotaResponseMode, QuotaRetryable, QuotaUsageLimit)
	}
	if err := ValidateTimeouts("routing.timeouts", c.Timeouts); err != nil {
		return err
	}
	if err := c.Ignition.Validate(); err != nil {
		return err
	}
	return ValidateChains(c.Chains)
}

const maxTimeoutMS = 30 * 24 * 60 * 60 * 1000

func ValidateTimeouts(prefix string, t *protocol.Timeouts) error {
	if t == nil {
		return nil
	}
	for _, f := range []struct {
		name string
		v    *int64
	}{
		{"header_ms", t.HeaderMS},
		{"idle_ms", t.IdleMS},
		{"total_ms", t.TotalMS},
	} {
		if f.v != nil && (*f.v < 0 || *f.v > maxTimeoutMS) {
			return fmt.Errorf("%s.%s must be between 0 and %d ms (30 days)", prefix, f.name, maxTimeoutMS)
		}
	}
	return nil
}

type Stream interface {
	Next() llm.Event
	Close()
}

type AttemptFunc func(ctx context.Context, extension string, account protocol.Account, req llm.Request) Stream

type Request struct {
	LLM           llm.Request
	Tenant        string
	Session       string
	Pin           string
	Endpoint      bool
	AnyModel      bool
	Attempt       AttemptFunc
	Attempting    func(Attempt)
	AttemptFailed func(llm.Tried)
}

type Attempt struct {
	Account, Extension string
	Public             string
	Model              string
	Step               int
	Why                string
}

const (
	WhyPinned   = "pinned"
	WhySession  = "session"
	WhyOnly     = "only"
	WhyFirst    = "first"
	WhyOnlyFree = "only-free"
	WhyBackup   = "backup"
)

type Credentials interface {
	Credential(ctx context.Context, extension, accountID string) (*protocol.Credential, uint64, *llm.Error)
	Rejected(ctx context.Context, accountID string, version uint64) bool
}

type Router struct {
	cfg      Config
	attempt  AttemptFunc
	state    *State
	picker   *picker
	affinity *affinity
	ttl      time.Duration
	gen      uint64
	created  time.Time

	mu        sync.RWMutex
	accounts  []*Account
	known     []*Account
	creds     Credentials
	readings  Readings
	catalog   CatalogFunc
	catalogue CatalogueFunc
	lists     catalogs

	now           func() time.Time
	sleep         func(ctx context.Context, d time.Duration) error
	rand          func() float64
	beforeReserve func(account string)
}

func New(cfg Config, accounts []Account, attempt AttemptFunc, state *State) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if state == nil {
		state = NewState()
	}
	r := &Router{cfg: cfg, attempt: attempt, state: state, gen: state.nextGen(), created: time.Now(), now: time.Now, sleep: sleepCtx, rand: rand.Float64}
	var err error
	if r.known, err = accountList(accounts, r.gen, state); err != nil {
		return nil, err
	}
	r.accounts = inService(r.known)
	if r.picker, err = newPicker(cfg.Strategy, r.accounts, state.rotation, r.gen); err != nil {
		return nil, err
	}
	if cfg.AffinityOn() {
		r.affinity, r.ttl = state.affinity, cfg.AffinityTTL()
	}
	return r, nil
}

func inService(accounts []*Account) []*Account {
	return slices.DeleteFunc(slices.Clone(accounts), func(a *Account) bool { return a.Held != "" })
}

func accountList(accounts []Account, gen uint64, state *State) ([]*Account, error) {
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
		a.gen, a.rev = gen, state.revision(a.ID)
		out = append(out, &a)
	}
	return out, nil
}

func (r *Router) SetAccounts(accounts []Account, same func(old, cur *Account) bool) error {
	known, err := accountList(accounts, r.gen, r.state)
	if err != nil {
		return err
	}
	list := inService(known)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range known {
		a.rev = r.state.revision(a.ID)
	}
	old := r.accounts
	var changed, fresh []*Account
	kept := map[string]bool{}
	for _, a := range old {
		i := slices.IndexFunc(list, func(b *Account) bool { return b.ID == a.ID })
		switch {
		case i < 0:
		case same != nil && !same(a, list[i]):
			changed, fresh = append(changed, a), append(fresh, list[i])
		default:
			kept[a.endpoint()] = true
		}
	}
	r.state.restart(changed, fresh, kept)
	r.accounts, r.known = list, known
	r.picker.setOrder(list)
	r.lists.retain(known, changed, same == nil)
	for _, a := range old {
		if !slices.ContainsFunc(list, func(b *Account) bool { return b.ID == a.ID }) {
			r.state.forget(a.ID)
		}
	}
	return nil
}

func (r *Router) Accounts() []Account {
	accounts, _ := r.snapshot()
	out := make([]Account, len(accounts))
	for i, a := range accounts {
		out[i] = *a
	}
	return out
}

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

func (r *Router) knownSnapshot() ([]*Account, Credentials) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.known, r.creds
}

func (r *Router) routed(accounts []*Account) []*Account {
	if r.picker.strategy != StrategyWeighted {
		return accounts
	}
	return slices.DeleteFunc(slices.Clone(accounts), func(a *Account) bool { return a.weight() == 0 })
}

func split(targets []target) (serving, held []target) {
	for _, t := range targets {
		if t.a.Held != "" {
			held = append(held, t)
		} else {
			serving = append(serving, t)
		}
	}
	return serving, held
}

func (r *Router) catalogFunc() CatalogFunc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.catalog
}

func (r *Router) catalogueFunc() CatalogueFunc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.catalogue
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

func (r *Router) Generate(ctx context.Context, req Request) Stream {
	return &call{r: r, ctx: ctx, req: req}
}

type call struct {
	r   *Router
	ctx context.Context
	req Request

	started bool
	final   *llm.Event
	cur     Stream
	at      target
	seq     uint64
	tried   []triedAttempt
	steps   []Step
	chain   bool
	capable []target
	held    []target
	ended   bool

	creds   Credentials
	renewed map[string]bool
	fresh   bool
	again   *target
	why     string
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
		if ev.Type == llm.EventFinish {
			c.r.state.success(c.at.a, c.at.model, c.seq)
		} else {
			c.r.failure(c.at.a, c.at.model, c.seq, ev.Error)
			if u := c.usageLimit(ev.Error); u != nil {
				ev.Error = u
			}
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
		c.r.state.end(c.at.a, c.seq)
	}
}

func (c *call) anyModel() bool {
	return c.req.AnyModel && c.req.LLM.Model == ""
}

func (c *call) route() llm.Event {
	ev := c.attempts()
	if ev.Type == llm.EventError && c.cur == nil {
		if u := c.usageLimit(ev.Error); u != nil {
			ev.Error = u
		}
		ev.Error = c.explained(ev.Error)
		if x := c.exhausted(ev.Error); x != nil {
			e := *ev.Error
			e.Exhausted = x
			ev.Error = &e
		}
	}
	return ev
}

func (c *call) attempts() llm.Event {
	r, model := c.r, c.req.LLM.Model
	accounts, creds := r.knownSnapshot()
	c.creds = creds
	var eligible []*Account
	for _, a := range r.routed(accounts) {
		if c.req.Pin != "" && !c.pinned(a) {
			continue
		}
		eligible = append(eligible, a)
	}
	if c.anyModel() {
		c.steps = []Step{{}}
		for _, a := range eligible {
			c.capable = append(c.capable, target{a: a})
		}
		c.capable, c.held = split(c.capable)
	} else {
		served := r.served(c.ctx, eligible, creds, r.catalogFunc())
		c.steps, c.chain = r.stepsFor(model)
		c.capable, c.held = split(r.targets(c.steps, eligible, served))
	}
	capable := c.capable
	if len(capable) == 0 {
		var msg string
		switch {
		case len(c.held) != 0:
			msg = heldMessage(c.held, model, c.req.Pin)
		case c.req.Pin != "":
			msg = fmt.Sprintf("account %q does not exist or does not serve model %q", c.req.Pin, model)
		default:
			msg = r.notServed(c.ctx, model)
		}
		return errorEvent(&llm.Error{Code: llm.CodeNotFound, Message: msg, ProviderCode: "model_not_found"})
	}

	tried := map[targetKey]bool{}
	used := map[string]bool{}
	attempts, rounds := 0, 0
	timeouts := 0
	rounded := r.cfg.RequestRetry != nil || slices.ContainsFunc(capable, func(t target) bool { return t.a.RequestRetry != nil })
	limit := func(a *Account) int {
		if a.RequestRetry != nil {
			return *a.RequestRetry
		}
		if r.cfg.RequestRetry != nil {
			return *r.cfg.RequestRetry
		}
		return 0
	}
	limited := map[string]time.Time{}
	budget := time.Duration(r.cfg.MaxWaitMS) * time.Millisecond
	var last *llm.Error
	for {
		now := r.now()
		var cands []target
		var next time.Time
		var nextReason *llm.Error
		var nextRound time.Time
		more := false
		busy := false
		for _, t := range capable {
			a := t.a
			if r.state.IsBanned(a.ID) {
				continue
			}
			if rounded && limit(a) < rounds {
				continue
			}
			until, reason, full := r.state.availability(a, t.model, now)
			if at, ok := limited[a.ID]; ok && r.cooling(a) {
				if floor := at.Add(roundRateLimitFloor); floor.After(now) && until.Before(floor) {
					until = floor
				}
			}
			switch {
			case full:
				busy = true
			case !until.IsZero():
				if next.IsZero() || until.Before(next) {
					next, nextReason = until, reason
				}
			case tried[t.key()] || r.cfg.MaxAccounts > 0 && len(used) >= r.cfg.MaxAccounts && !used[a.ID]:
				next, nextReason = now, nil
				until = now
			default:
				cands = append(cands, t)
			}
			if rounded && !full && !until.IsZero() && limit(a) > rounds {
				if !more || until.Before(nextRound) {
					nextRound = until
				}
				more = true
			}
		}
		if r.cfg.MaxAttempts > 0 && attempts >= r.cfg.MaxAttempts {
			return errorEvent(last)
		}
		if len(cands) > 0 {
			var t target
			if c.again != nil && slices.Contains(cands, *c.again) {
				t = *c.again
			} else {
				t, c.why = c.choose(cands, now)
			}
			c.again = nil
			a := t.a
			if r.beforeReserve != nil {
				r.beforeReserve(a.ID)
			}
			seq, ok := r.state.reserve(a, t.model, r.now())
			if !ok {
				continue
			}
			attempts++
			tried[t.key()], used[a.ID] = true, true
			ev, started := c.try(t, seq)
			if started {
				return ev
			}
			last = ev.Error
			failed := llm.Tried{Account: r.state.PublicID(a.ID), Provider: a.Extension, Model: t.model, Result: ResultCode(last)}
			c.tried = append(c.tried, triedAttempt{t.key(), failed})
			if c.req.AttemptFailed != nil {
				c.req.AttemptFailed(failed)
			}
			if rounded && (last.Code == llm.CodeRateLimit || last.Code == llm.CodeQuota) {
				limited[a.ID] = r.now()
			}
			if c.fresh {
				c.fresh = false
				delete(tried, t.key())
				c.again = &t
				continue
			}
			if !retrySafe(last) {
				return ev
			}
			if last.IsTimeout() {
				timeouts++
				if timeouts > 1 {
					return errorEvent(last)
				}
			}
			continue
		}
		if next.IsZero() {
			if last != nil {
				return c.noAccountEvent(last)
			}
			if busy {
				return errorEvent(&llm.Error{Code: llm.CodeRateLimit, Status: 429, ProviderCode: "accounts_busy",
					Message: fmt.Sprintf("every account for model %q is at its concurrency limit", model)})
			}
			if e := c.banned(); e != nil {
				return errorEvent(e)
			}
			return errorEvent(&llm.Error{Code: llm.CodeUnavailable, Message: fmt.Sprintf("no account can serve model %q", model)})
		}
		wait := max(next.Sub(now), 0)
		if rounded {
			if !more || last != nil && (last.Code == llm.CodeAuth || last.Code == llm.CodeNotFound) {
				if last != nil {
					return errorEvent(last)
				}
				return errorEvent(coolingError(model, wait, nextReason))
			}
			wait = max(nextRound.Sub(now), 0)
			budget = time.Duration(r.cfg.MaxWaitMS) * time.Millisecond
		} else if wait <= 0 && last != nil {
			return c.noAccountEvent(last)
		}
		if wait > budget {
			if last != nil {
				return errorEvent(last)
			}
			return errorEvent(coolingError(model, wait, nextReason))
		}
		jitter := min(time.Duration(r.rand()*float64(wait/4)), 2*time.Second, budget-wait)
		wait += jitter
		if wait > 0 {
			if err := r.sleep(c.ctx, wait); err != nil {
				return errorEvent(&llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"})
			}
		}
		budget -= wait
		rounds++
		clear(tried)
		clear(used)
	}
}

func (c *call) usageLimit(e *llm.Error) *llm.Error {
	r := c.r
	if r.cfg.QuotaResponseMode != QuotaUsageLimit || e == nil || len(c.capable) == 0 ||
		e.Code != llm.CodeQuota && e.Code != llm.CodeRateLimit && e.Status != http.StatusTooManyRequests {
		return nil
	}
	now := r.now()
	var reset time.Time
	var window int64
	var cause *llm.Error
	provider := c.capable[0].a.Extension
	for _, t := range c.capable {
		until, w, why, ok := r.state.quotaOut(t.a, t.model, now)
		if !ok {
			return nil
		}
		if reset.IsZero() || until.Before(reset) {
			reset, window, cause = until, w, why
		}
		if t.a.Extension != provider {
			provider = ""
		}
	}
	if reset.Sub(now) <= usageLimitMinReset {
		return nil
	}
	if e.ProviderCode == "model_cooldown" && cause != nil {
		e = cause
	}
	if i := strings.LastIndexByte(provider, '/'); i >= 0 {
		provider = provider[i+1:]
	}
	u := &llm.UsageLimit{Model: c.req.LLM.Model, Provider: provider, ResetsAt: reset, WindowMinutes: window,
		LastError: errorSummary(e), At: now}
	return &llm.Error{Code: llm.CodeQuota, Status: http.StatusTooManyRequests, ProviderCode: "usage_limit_reached",
		Message: u.Message(), Scope: llm.ScopeCredential, RetryAfterMS: (reset.Sub(now) + time.Millisecond - 1).Milliseconds(),
		UsageLimit: u}
}

func errorSummary(e *llm.Error) string {
	code, msg := e.ProviderCode, e.Message
	var b struct {
		Error               json.RawMessage
		Code, Type, Message json.RawMessage
	}
	if json.Unmarshal(e.Raw, &b) == nil {
		var c, m string
		if len(b.Error) > 0 {
			var o struct{ Code, Type, Message json.RawMessage }
			if json.Unmarshal(b.Error, &o) == nil {
				c, m = cmp.Or(jsonText(o.Code), jsonText(o.Type)), jsonText(o.Message)
			} else {
				m = jsonText(b.Error)
			}
		}
		if c == "" && m == "" {
			c, m = cmp.Or(jsonText(b.Code), jsonText(b.Type)), jsonText(b.Message)
		}
		if c != "" || m != "" {
			code, msg = c, m
		}
	}
	s := msg
	switch {
	case code != "" && msg == "":
		s = code
	case code != "" && !strings.Contains(strings.ToLower(msg), strings.ToLower(code)):
		s = code + ": " + msg
	}
	return sanitizeSummary(s)
}

// Gemini's error code is the number 429.
func jsonText(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return strings.TrimSpace(s)
	}
	var x any
	if json.Unmarshal(v, &x) == nil {
		switch x.(type) {
		case float64, bool:
			return strings.TrimSpace(string(v))
		}
	}
	return ""
}

func (c *call) choose(cands []target, now time.Time) (chosen target, why string) {
	r := c.r
	if c.req.Pin != "" {
		why = WhyPinned
	}
	if r.affinity != nil && c.req.Session != "" {
		if b, ok := r.affinity.lookup(c.affinityKey(), now); ok {
			for _, t := range cands {
				if t.a.ID == b.account && t.model == b.model {
					return t, cmp.Or(why, WhySession)
				}
			}
		}
	}
	step := cands[0].step
	top := cands[0].a.Priority
	for _, t := range cands {
		if t.step == step {
			top = max(top, t.a.Priority)
		}
	}
	var best []*Account
	for _, t := range cands {
		if t.step == step && t.a.Priority == top {
			best = append(best, t.a)
		}
	}
	a := r.picker.pick(rotationKey(c.req.LLM.Model, c.chain, step), best, r.state.activeCount, r.state.rank, now)
	i := slices.IndexFunc(cands, func(t target) bool { return t.step == step && t.a == a })
	return cands[i], cmp.Or(why, c.whyChosen(step, top, len(best)))
}

func (c *call) whyChosen(step, chosen, n int) string {
	serving := stepTargetsOf(c.capable, step)
	if len(serving) == 1 {
		return WhyOnly
	}
	top, inTop := serving[0].a.Priority, 0
	for _, t := range serving {
		top = max(top, t.a.Priority)
	}
	for _, t := range serving {
		if t.a.Priority == top {
			inTop++
		}
	}
	switch {
	case chosen < top:
		return WhyBackup
	case inTop == 1:
		return WhyFirst
	case n == 1:
		return WhyOnlyFree
	}
	return c.r.picker.strategy
}

func (c *call) pinned(a *Account) bool {
	return a.ID == c.req.Pin || c.r.state.PublicID(a.ID) == c.req.Pin
}

func (c *call) affinityKey() affinityKey {
	return affinityKey{tenant: c.req.Tenant, model: c.req.LLM.Model, session: c.req.Session}
}

func (c *call) try(t target, seq uint64) (ev llm.Event, started bool) {
	r, a := c.r, t.a
	if c.req.Attempting != nil {
		c.req.Attempting(Attempt{Account: a.ID, Extension: a.Extension, Public: r.state.PublicID(a.ID), Model: t.model, Step: c.stepNumber(t), Why: c.why})
	}
	call := a.call()
	var version uint64
	if a.Managed {
		var e *llm.Error
		if c.creds == nil {
			e = &llm.Error{Code: llm.CodeUnavailable, Scope: llm.ScopeCredential, Message: "the account has no credential store"}
		} else {
			call.Credential, version, e = c.creds.Credential(c.ctx, a.Extension, a.ID)
		}
		if e != nil {
			r.failure(a, t.model, seq, e)
			r.state.end(a, seq)
			return errorEvent(e), false
		}
	}
	attempt := r.attempt
	if c.req.Attempt != nil {
		attempt = c.req.Attempt
	}
	req := Renamed(c.req.LLM, t.model)
	req.Caller = caller(a.Extension, c.req.Tenant)
	s := attempt(c.ctx, a.Extension, call, req)
	ev = s.Next()
	if ev.Type == llm.EventError {
		s.Close()
		if a.Managed && c.creds != nil && credentialRefused(ev.Error) && !c.renewed[a.ID] {
			if c.renewed == nil {
				c.renewed = map[string]bool{}
			}
			c.renewed[a.ID] = true
			if c.creds.Rejected(c.ctx, a.ID, version) {
				c.fresh = true
				r.state.end(a, seq)
				return ev, false
			}
		}
		r.failure(a, t.model, seq, ev.Error)
		r.state.end(a, seq)
		return ev, false
	}
	c.cur, c.at, c.seq = s, t, seq
	c.req.LLM = llm.Request{Model: c.req.LLM.Model}
	if r.affinity != nil && c.req.Session != "" {
		r.affinity.bind(c.affinityKey(), a, t.model, r.now(), r.ttl)
	}
	if ev.Terminal() {
		c.r.state.success(a, t.model, seq)
		c.release()
	}
	return ev, true
}

const roundRateLimitFloor = 10 * time.Second

func (r *Router) cooling(a *Account) bool {
	if a.DisableCooling != nil {
		return !*a.DisableCooling
	}
	return !r.cfg.DisableCooling
}

func (r *Router) failure(a *Account, model string, seq uint64, e *llm.Error) {
	if (e == nil || e.Code != llm.CodeAccountClosed) && !r.cooling(a) {
		return
	}
	r.state.failure(a, model, seq, e, r.now(), time.Duration(r.cfg.TransientErrorCooldownMS)*time.Millisecond)
}

func retrySafe(e *llm.Error) bool {
	if e == nil || !e.Retryable {
		return false
	}
	if e.Code == llm.CodeAccountClosed {
		return e.Scope != llm.ScopeRequest
	}
	if e.Code == llm.CodeUnsupported {
		return e.Scope != "" && e.Scope != llm.ScopeRequest
	}
	kind, _, _, _, _ := classify(e)
	return kind != ""
}

func credentialRefused(e *llm.Error) bool {
	return e != nil && e.Code == llm.CodeAuth && (e.Scope == "" || e.Scope == llm.ScopeCredential)
}

func coolingError(model string, wait time.Duration, reason *llm.Error) *llm.Error {
	if wait <= 0 {
		wait = time.Second
	}
	e := &llm.Error{
		Code:         llm.CodeRateLimit,
		Status:       429,
		ProviderCode: "model_cooldown",
		Message:      fmt.Sprintf("every account for model %q is cooling down; try again in %s", model, llm.WholeSeconds(wait)),
		RetryAfterMS: (wait + time.Millisecond - 1).Milliseconds(),
	}
	if reason != nil && reason.Code == llm.CodeQuota {
		e.Code = llm.CodeQuota
	}
	return e
}

const maxServedNamed = 8

func (r *Router) notServed(ctx context.Context, model string) string {
	msg := fmt.Sprintf("no account serves model %q", model)
	var names []string
	for _, m := range r.ServedModels(ctx) {
		names = append(names, m.ID)
	}
	if len(names) == 0 {
		return msg + ". pluxy serves no models yet: add an account first."
	}
	more := ""
	if len(names) > maxServedNamed {
		more = fmt.Sprintf(" and %d more (pluxy model list shows them all)", len(names)-maxServedNamed)
		names = names[:maxServedNamed]
	}
	return fmt.Sprintf("%s. pluxy serves: %s%s. Set the tool's model to one of them, or add a model alias to an account so that %q goes to one of them.",
		msg, strings.Join(names, ", "), more, model)
}

func heldMessage(held []target, model, pin string) string {
	state := held[0].a.Held
	for _, t := range held[1:] {
		if t.a.Held != state {
			state = ""
		}
	}
	verb := "is turned off or needs a new sign-in"
	switch state {
	case HealthDisabled:
		verb = "is turned off"
	case HealthNeedsLogin:
		verb = "needs a new sign-in"
	}
	if pin != "" {
		return fmt.Sprintf("account %q cannot serve model %q: it %s", pin, model, verb)
	}
	return fmt.Sprintf("no account can serve model %q: every account that serves it %s", model, verb)
}

func (c *call) banned() *llm.Error {
	var reason *llm.Error
	for _, t := range c.capable {
		e := c.r.state.banReason(t.a.ID)
		if e == nil {
			return nil
		}
		if reason == nil {
			reason = e
		}
	}
	if reason == nil {
		return nil
	}
	ban := *reason
	return &ban
}

func (c *call) noAccountEvent(last *llm.Error) llm.Event {
	if !c.req.Endpoint || last == nil || last.Code != llm.CodeUnsupported || last.Scope == "" || last.Scope == llm.ScopeRequest || last.Status == http.StatusBadRequest {
		return errorEvent(last)
	}
	return errorEvent(&llm.Error{
		Code:         llm.CodeUnavailable,
		Status:       http.StatusServiceUnavailable,
		ProviderCode: "auth_not_found",
		Message:      "no auth available",
	})
}

func errorEvent(e *llm.Error) llm.Event {
	return llm.Event{Type: llm.EventError, Error: e}
}
