package route

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

const PublicKeyLen = 32

func NewPublicKey() []byte {
	b := make([]byte, PublicKeyLen)
	_, _ = rand.Read(b)
	return b
}

func (s *State) SetPublicKey(key []byte) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	s.pubKey = slices.Clone(key)
}

func (s *State) PublicID(id string) string {
	if isPublicID(id) {
		return id
	}
	s.keyMu.RLock()
	m := hmac.New(sha256.New, s.pubKey)
	s.keyMu.RUnlock()
	m.Write([]byte(id))
	return "acc_" + hex.EncodeToString(m.Sum(nil)[:4])
}

func isPublicID(id string) bool {
	hexDigits, ok := strings.CutPrefix(id, "acc_")
	if !ok || len(hexDigits) != 8 {
		return false
	}
	for _, c := range hexDigits {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (c *Config) ExplainOn() bool { return c.Explain == nil || *c.Explain }

var resultCodes = []string{llm.CodeInvalidRequest, llm.CodeAuth, llm.CodePermission, llm.CodeAccountClosed,
	llm.CodeNotFound, llm.CodeRateLimit, llm.CodeQuota, llm.CodeUnsupported, llm.CodeUpstream, llm.CodeUnavailable,
	llm.CodeCancelled, llm.CodeInternal, llm.CodeExtensionCrashed, llm.CodeExtensionProtocol, llm.CodeExtensionUnavailable}

func ResultCode(e *llm.Error) string {
	if e != nil && slices.Contains(resultCodes, e.Code) {
		return e.Code
	}
	return "error"
}

const (
	LeftBusy    = "busy"
	LeftSkipped = "skipped"
)

type leftEntry struct {
	t target
	llm.LeftOut
}

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

func stepTargetsOf(targets []target, i int) []target {
	var out []target
	for _, t := range targets {
		if t.step == i {
			out = append(out, t)
		}
	}
	return out
}

func (r *Router) heldLeftOut(t target, named bool) llm.LeftOut {
	l := llm.LeftOut{Account: r.state.PublicID(t.a.ID), State: t.a.Held}
	if named {
		l.Provider, l.Model = t.a.Extension, t.model
	}
	return l
}

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
		e.Tried[len(e.Tried)-1].ID = t.key.id
	}
	for _, l := range r.leftOut(c.capable, func(t target) bool { return tried[t.key()] }, c.chain, now) {
		l.LeftOut.ID = l.t.a.ID
		e.LeftOut = append(e.LeftOut, l.LeftOut)
	}
	for _, t := range c.held {
		l := r.heldLeftOut(t, c.chain)
		l.ID = t.a.ID
		e.LeftOut = append(e.LeftOut, l)
	}
	return e
}

type triedAttempt struct {
	key targetKey
	llm.Tried
}

func (c *call) explained(e *llm.Error) *llm.Error {
	if e == nil || e.Routed != nil {
		return e
	}
	x := *e
	x.Routed = c.explain()
	x.Routed.Model = c.req.LLM.Model
	if c.r.cfg.ExplainOn() {
		x.Explain = public(x.Routed)
	}
	return &x
}

func public(x *llm.Explain) *llm.Explain {
	p := &llm.Explain{Trace: x.Trace, Tried: slices.Clone(x.Tried), LeftOut: slices.Clone(x.LeftOut)}
	for i := range p.Tried {
		p.Tried[i].ID = ""
	}
	for i := range p.LeftOut {
		p.LeftOut[i].ID = ""
	}
	return p
}

type Plan struct {
	Model   string        `json:"model"`
	Chain   []Step        `json:"chain,omitempty"`
	Order   []PlanStep    `json:"order"`
	LeftOut []PlanLeftOut `json:"left_out"`
}

type PlanStep struct {
	ID       string `json:"id"`
	Step     int    `json:"step,omitempty"`
	Account  string `json:"account"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Rank     *Rank  `json:"rank,omitempty"`
}

type PlanLeftOut struct {
	ID   string `json:"id"`
	Step int    `json:"step,omitempty"`
	llm.LeftOut
}

func (r *Router) Plan(ctx context.Context, model string) Plan {
	accounts, creds := r.knownSnapshot()
	eligible := r.routed(accounts)
	served := r.served(ctx, eligible, creds, r.catalogFunc())
	steps, chain := r.stepsFor(model)
	capable, held := split(r.targets(steps, eligible, served))
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
				continue
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
			a := preview.pick(key, best, r.state.activeCount, r.state.rank, now)
			t := capable[slices.IndexFunc(capable, func(t target) bool { return t.step == i && t.a == a })]
			ps := PlanStep{ID: a.ID, Step: number(t), Account: r.state.PublicID(a.ID), Provider: a.Extension, Model: t.model}
			if r.picker.strategy == StrategySoonestReset {
				rank := r.state.rank(a, now)
				ps.Rank = &rank
			}
			p.Order = append(p.Order, ps)
			ordered[t.key()] = true
			cands = slices.DeleteFunc(cands, func(b *Account) bool { return b == a })
		}
	}
	for _, l := range r.leftOut(capable, func(t target) bool { return ordered[t.key()] }, chain, now) {
		p.LeftOut = append(p.LeftOut, PlanLeftOut{ID: l.t.a.ID, Step: number(l.t), LeftOut: l.LeftOut})
	}
	for _, t := range held {
		p.LeftOut = append(p.LeftOut, PlanLeftOut{ID: t.a.ID, Step: number(t), LeftOut: r.heldLeftOut(t, chain)})
	}
	return p
}
