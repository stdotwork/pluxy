package route

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Public account IDs. The ID an account has in the config or the store is chosen by a person,
// or comes from the name of an old core's auth file, which holds an email address; it must not
// travel in a reply to a client. A reply names an account by its public ID instead: a keyed
// digest of the ID, so it is short, stable across restarts (the key is kept with core's state)
// and says nothing about the account to anyone without the key.

// PublicKeyLen is the length in bytes of the key public IDs are made with.
const PublicKeyLen = 32

// NewPublicKey returns a new random key for SetPublicKey.
func NewPublicKey() []byte {
	b := make([]byte, PublicKeyLen)
	_, _ = rand.Read(b)
	return b
}

// SetPublicKey sets the key public IDs are made with. State starts with a random key of its own,
// which core replaces with the one it keeps, before any router uses the state.
func (s *State) SetPublicKey(key []byte) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	s.pubKey = slices.Clone(key)
}

// PublicID returns the public ID of account id: "acc_" and 8 hex digits.
func (s *State) PublicID(id string) string {
	s.keyMu.RLock()
	m := hmac.New(sha256.New, s.pubKey)
	s.keyMu.RUnlock()
	m.Write([]byte(id))
	return "acc_" + hex.EncodeToString(m.Sum(nil)[:4])
}

// Config.Explain: whether replies name the account and errors say what was tried.

// ExplainOn reports whether replies carry the account headers and errors the pluxy member:
// routing.explain, on unless it is false.
func (c *Config) ExplainOn() bool { return c.Explain == nil || *c.Explain }

// Result codes of an attempt, as the pluxy member has them: the error's code when it is one of
// core's own, else "error". The code of an error an extension sent is checked against this list,
// so a reply never carries text an extension or an upstream chose.
var resultCodes = []string{llm.CodeInvalidRequest, llm.CodeAuth, llm.CodePermission, llm.CodeAccountClosed,
	llm.CodeNotFound, llm.CodeRateLimit, llm.CodeQuota, llm.CodeUnsupported, llm.CodeUpstream, llm.CodeUnavailable,
	llm.CodeCancelled, llm.CodeInternal, llm.CodeExtensionCrashed, llm.CodeExtensionProtocol, llm.CodeExtensionUnavailable}

func resultCode(e *llm.Error) string {
	if e != nil && slices.Contains(resultCodes, e.Code) {
		return e.Code
	}
	return "error"
}

// Left-out states besides the health states (Health): the account was at its concurrency limit,
// or the request's limits (max_attempts, max_accounts) ended it before the account's turn.
const (
	LeftBusy    = "busy"
	LeftSkipped = "skipped"
)

// Held is an account that serves no requests because a person turned it off or the provider
// refused its sign-in: core's credential store knows it, the router does not route over it.
type Held struct {
	Account Account
	// State is HealthDisabled or HealthNeedsLogin.
	State string
}

// SetHeld sets where the accounts that are not routed over come from, to be named in what a
// request left out. nil: none.
func (r *Router) SetHeld(f func() []Held) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held = f
}

// leftEntry is a target a request left out, and why.
type leftEntry struct {
	t target
	llm.LeftOut
}

// leftOut returns, for each of targets, in order, why it was not used: its account's health now,
// or busy or skipped when its health is ok. skip names targets to leave out of the list (those
// tried or ordered). With named, each entry also names the account's provider and model, as a
// request through a chain names them, since an account can be in two steps.
func (r *Router) leftOut(targets []target, skip func(target) bool, named bool, now time.Time) []leftEntry {
	var out []leftEntry
	seen := map[targetKey]bool{}
	for _, t := range targets {
		if skip(t) || seen[t.key()] {
			continue
		}
		seen[t.key()] = true
		a := t.a
		h := r.state.Health(*a, now, Hold{})
		state, until := h.State, h.Until
		if state == HealthOK {
			// Health leaves out a block on a model the account does not have; this request's
			// model may be that one.
			blockedUntil, _, busy := r.state.availability(a, t.model, now)
			switch {
			case !blockedUntil.IsZero():
				state, until = HealthCooling, &blockedUntil
			case busy:
				state = LeftBusy
			default:
				state = LeftSkipped
			}
		}
		l := llm.LeftOut{Account: r.state.PublicID(a.ID), State: state, Until: until}
		if named {
			l.Provider, l.Model = a.Extension, t.model
		}
		out = append(out, leftEntry{t, l})
	}
	return out
}

// heldFor returns the held accounts step s, the step at index i, would have used: those the step
// names or whose provider it names, and whose own model list names its model; and those with no
// list of their own that share an extension with an account in serving, the step's accounts.
// Nothing says what else an account without a list serves. With oneProvider, a step that names no
// provider and no accounts is served by one provider: the one serving names, when it names one.
func (r *Router) heldFor(s Step, serving []target, oneProvider bool) []Held {
	r.mu.RLock()
	f := r.held
	r.mu.RUnlock()
	if f == nil {
		return nil
	}
	var out []Held
	for _, h := range f() {
		a := h.Account
		switch {
		case s.Provider != "" && a.Extension != s.Provider,
			len(s.Accounts) > 0 && !slices.Contains(s.Accounts, a.ID),
			oneProvider && s.Provider == "" && len(s.Accounts) == 0 && len(serving) > 0 && a.Extension != serving[0].a.Extension:
			continue
		}
		if _, ok := a.resolve(s.Model, r.cfg.ForceModelPrefix, nil); !ok {
			continue
		}
		if len(a.Models) == 0 && !slices.ContainsFunc(serving, func(t target) bool { return t.a.Extension == a.Extension }) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// stepTargetsOf returns the targets of targets that are for step i.
func stepTargetsOf(targets []target, i int) []target {
	var out []target
	for _, t := range targets {
		if t.step == i {
			out = append(out, t)
		}
	}
	return out
}

// heldLeftOut is the entry of a held account h for step s.
func (r *Router) heldLeftOut(h Held, s Step, named bool) llm.LeftOut {
	l := llm.LeftOut{Account: r.state.PublicID(h.Account.ID), State: h.State}
	if named {
		l.Provider, l.Model = h.Account.Extension, s.Model
		if up, ok := h.Account.resolve(s.Model, r.cfg.ForceModelPrefix, nil); ok && up != "" {
			l.Model = up
		}
	}
	return l
}

// explain says what the call did and left out, for the error that ends it.
func (c *call) explain() *llm.Explain {
	r := c.r
	now := r.now()
	tried := map[targetKey]bool{}
	for _, t := range c.tried {
		tried[t.key] = true
	}
	e := &llm.Explain{Tried: []llm.Tried{}, LeftOut: []llm.LeftOut{}}
	for _, t := range c.tried {
		e.Tried = append(e.Tried, t.Tried)
	}
	for _, l := range r.leftOut(c.capable, func(t target) bool { return tried[t.key()] }, c.chain, now) {
		e.LeftOut = append(e.LeftOut, l.LeftOut)
	}
	// A request that names no model is served by every provider.
	for i, s := range c.steps {
		for _, h := range r.heldFor(s, stepTargetsOf(c.capable, i), !c.anyModel()) {
			e.LeftOut = append(e.LeftOut, r.heldLeftOut(h, s, c.chain))
		}
	}
	return e
}

// triedAttempt is an attempt as the call keeps it: its account's real ID and its step, for
// matching.
type triedAttempt struct {
	key targetKey
	llm.Tried
}

// explained returns a copy of e that carries the call's explanation; e itself may be held by
// routing state.
func (c *call) explained(e *llm.Error) *llm.Error {
	if e == nil || e.Explain != nil {
		return e
	}
	x := *e
	x.Explain = c.explain()
	return &x
}

// Plan is what a request for a model would do now, as GET /route shows it. It sends nothing.
type Plan struct {
	Model string `json:"model"`
	// Chain is the model's fallback chain, when it has one; the accounts below then say which of
	// its steps they are in.
	Chain []Step `json:"chain,omitempty"`
	// Order are the accounts in the order the router would try them.
	Order []PlanStep `json:"order"`
	// LeftOut are the accounts that serve the model and would not be tried now, and why.
	LeftOut []PlanLeftOut `json:"left_out"`
}

// PlanStep is one account a request would try.
type PlanStep struct {
	// ID is the account's ID, which only the management API shows; Account is its public ID.
	ID string `json:"id"`
	// Step is the chain's step the account is tried for, from 1; absent with no chain.
	Step     int    `json:"step,omitempty"`
	Account  string `json:"account"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// PlanLeftOut is an account a request would leave out.
type PlanLeftOut struct {
	ID string `json:"id"`
	// Step is the chain's step the account would serve, from 1; absent with no chain.
	Step int `json:"step,omitempty"`
	llm.LeftOut
}

// Plan says what a request for model would do now: the accounts it would try in order, step by
// step through the model's chain, and those it would leave out. It runs the router's own steps
// (the accounts that serve each step, the blocks that hold them, priority, then the strategy's
// turns, on a copy of its memory) and sends nothing, so asking changes no routing state.
func (r *Router) Plan(ctx context.Context, model string) Plan {
	accounts, creds := r.snapshot()
	var eligible []*Account
	for _, a := range accounts {
		if r.picker.strategy == StrategyWeighted && a.weight() == 0 {
			continue
		}
		eligible = append(eligible, a)
	}
	served := r.served(ctx, eligible, creds, r.catalogFunc())
	steps, chain := r.stepsFor(model)
	capable := r.targets(steps, eligible, served)
	now := r.now()
	p := Plan{Model: model, Order: []PlanStep{}, LeftOut: []PlanLeftOut{}}
	if chain {
		p.Chain = slices.Clone(steps)
	}
	number := func(t target) int {
		if !chain {
			return 0
		}
		return t.step + 1
	}
	ordered := map[targetKey]bool{}
	for i := range steps {
		var cands []*Account
		for _, t := range stepTargetsOf(capable, i) {
			if ordered[t.key()] {
				continue // an earlier step's, with the same model
			}
			until, _, busy := r.state.availability(t.a, t.model, now)
			if !r.state.IsBanned(t.a.ID) && until.IsZero() && !busy {
				cands = append(cands, t.a)
			}
		}
		key := rotationKey(model, chain, i)
		preview := r.picker.preview(key)
		for len(cands) > 0 {
			top := cands[0].Priority
			for _, a := range cands {
				top = max(top, a.Priority)
			}
			var best []*Account
			for _, a := range cands {
				if a.Priority == top {
					best = append(best, a)
				}
			}
			a := preview.pick(key, best, r.state.activeCount, now)
			t := capable[slices.IndexFunc(capable, func(t target) bool { return t.step == i && t.a == a })]
			p.Order = append(p.Order, PlanStep{ID: a.ID, Step: number(t), Account: r.state.PublicID(a.ID), Provider: a.Extension, Model: t.model})
			ordered[t.key()] = true
			cands = slices.DeleteFunc(cands, func(b *Account) bool { return b == a })
		}
	}
	for _, l := range r.leftOut(capable, func(t target) bool { return ordered[t.key()] }, chain, now) {
		p.LeftOut = append(p.LeftOut, PlanLeftOut{ID: l.t.a.ID, Step: number(l.t), LeftOut: l.LeftOut})
	}
	for i, s := range steps {
		for _, h := range r.heldFor(s, stepTargetsOf(capable, i), true) {
			p.LeftOut = append(p.LeftOut, PlanLeftOut{ID: h.Account.ID, Step: number(target{step: i}), LeftOut: r.heldLeftOut(h, s, chain)})
		}
	}
	return p
}
