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

// leftOut returns, for each of accounts, in order, why it was not used: its health now, or busy
// or skipped when its health is ok. skip names accounts to leave out of the list (those tried).
// upstream gives each account's name for the model, for blocks on the model.
func (r *Router) leftOut(accounts []*Account, upstream map[string]string, skip func(string) bool, now time.Time) []llm.LeftOut {
	out := []llm.LeftOut{}
	for _, a := range accounts {
		if skip(a.ID) {
			continue
		}
		h := r.state.Health(*a, now, Hold{})
		state, until := h.State, h.Until
		if state == HealthOK {
			// Health leaves out a block on a model the account does not have; this request's
			// model may be that one.
			blockedUntil, _, busy := r.state.availability(a, upstream[a.ID], now)
			switch {
			case !blockedUntil.IsZero():
				state, until = HealthCooling, &blockedUntil
			case busy:
				state = LeftBusy
			default:
				state = LeftSkipped
			}
		}
		out = append(out, llm.LeftOut{Account: r.state.PublicID(a.ID), State: state, Until: until})
	}
	return out
}

// heldFor returns the held accounts a request for model would have used: those whose own model
// list names it, and those with no list of their own that share an extension with an account in
// serving. Nothing says what else an account without a list serves.
func (r *Router) heldFor(model string, serving []*Account) []Held {
	r.mu.RLock()
	f := r.held
	r.mu.RUnlock()
	if f == nil {
		return nil
	}
	var out []Held
	for _, h := range f() {
		a := h.Account
		if _, ok := a.resolve(model, r.cfg.ForceModelPrefix, nil); !ok {
			continue
		}
		if len(a.Models) == 0 && !slices.ContainsFunc(serving, func(b *Account) bool { return b.Extension == a.Extension }) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// explain says what the call did and left out, for the error that ends it.
func (c *call) explain() *llm.Explain {
	r := c.r
	now := r.now()
	tried := map[string]bool{}
	for _, t := range c.tried {
		tried[t.id] = true
	}
	e := &llm.Explain{Tried: []llm.Tried{}}
	for _, t := range c.tried {
		e.Tried = append(e.Tried, t.Tried)
	}
	e.LeftOut = r.leftOut(c.capable, c.upstream, func(id string) bool { return tried[id] }, now)
	for _, h := range r.heldFor(c.req.LLM.Model, c.capable) {
		e.LeftOut = append(e.LeftOut, llm.LeftOut{Account: r.state.PublicID(h.Account.ID), State: h.State})
	}
	return e
}

// triedAttempt is an attempt as the call keeps it: the account's real ID for matching.
type triedAttempt struct {
	id string
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
	// Order are the accounts in the order the router would try them.
	Order []PlanStep `json:"order"`
	// LeftOut are the accounts that serve the model and would not be tried now, and why.
	LeftOut []PlanLeftOut `json:"left_out"`
}

// PlanStep is one account a request would try.
type PlanStep struct {
	// ID is the account's ID, which only the management API shows; Account is its public ID.
	ID       string `json:"id"`
	Account  string `json:"account"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// PlanLeftOut is an account a request would leave out.
type PlanLeftOut struct {
	ID string `json:"id"`
	llm.LeftOut
}

// Plan says what a request for model would do now: the accounts it would try in order, and those
// it would leave out. It runs the router's own steps (the accounts that serve the model, the
// blocks that hold them, priority, then the strategy's turns, on a copy of its memory) and sends
// nothing, so asking changes no routing state.
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
	now := r.now()
	var capable, cands []*Account
	upstream := map[string]string{}
	for _, a := range eligible {
		if up, ok := a.resolve(model, r.cfg.ForceModelPrefix, served[a.ID]); ok {
			capable = append(capable, a)
			upstream[a.ID] = up
			until, _, busy := r.state.availability(a, up, now)
			if !r.state.IsBanned(a.ID) && until.IsZero() && !busy {
				cands = append(cands, a)
			}
		}
	}
	p := Plan{Model: model, Order: []PlanStep{}, LeftOut: []PlanLeftOut{}}
	preview := r.picker.preview(model)
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
		a := preview.pick(model, best, r.state.activeCount, now)
		p.Order = append(p.Order, PlanStep{ID: a.ID, Account: r.state.PublicID(a.ID), Provider: a.Extension, Model: upstream[a.ID]})
		cands = slices.DeleteFunc(cands, func(b *Account) bool { return b == a })
	}
	ordered := func(id string) bool { return slices.ContainsFunc(p.Order, func(s PlanStep) bool { return s.ID == id }) }
	byPublic := map[string]string{}
	for _, a := range capable {
		byPublic[r.state.PublicID(a.ID)] = a.ID
	}
	for _, l := range r.leftOut(capable, upstream, ordered, now) {
		p.LeftOut = append(p.LeftOut, PlanLeftOut{ID: byPublic[l.Account], LeftOut: l})
	}
	for _, h := range r.heldFor(model, capable) {
		p.LeftOut = append(p.LeftOut, PlanLeftOut{ID: h.Account.ID, LeftOut: llm.LeftOut{Account: r.state.PublicID(h.Account.ID), State: h.State}})
	}
	return p
}
