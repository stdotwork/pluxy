package route

import (
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

type Cooldown struct {
	Scope        string     `json:"scope"`
	Model        string     `json:"model,omitempty"`
	Kind         string     `json:"kind"`
	Until        time.Time  `json:"until"`
	BackoffLevel int        `json:"backoff_level,omitempty"`
	Error        *llm.Error `json:"error,omitempty"`
}

type Runtime struct {
	Cooldowns     []Cooldown
	CooldownUntil time.Time
	LastError     *llm.Error
	LastErrorAt   time.Time
	Active        int
}

var blockKinds = [numBlockKinds]string{blockRateLimit, blockQuota, blockCooldown}

func (s *State) Runtime(a Account, now time.Time) Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rt Runtime
	for k, st := range s.scopes {
		if !s.covers(k, &a) {
			continue
		}
		for i, b := range st.blocks {
			if !b.until.After(now) {
				continue
			}
			rt.Cooldowns = append(rt.Cooldowns, Cooldown{Scope: k.kind, Model: k.model, Kind: blockKinds[i], Until: b.until,
				BackoffLevel: b.level, Error: Brief(b.reason)})
			if k.kind != scopeModel && b.until.After(rt.CooldownUntil) {
				rt.CooldownUntil = b.until
			}
		}
	}
	slices.SortFunc(rt.Cooldowns, func(x, y Cooldown) int {
		if c := y.Until.Compare(x.Until); c != 0 {
			return c
		}
		if c := strings.Compare(x.Scope, y.Scope); c != 0 {
			return c
		}
		if c := strings.Compare(x.Model, y.Model); c != 0 {
			return c
		}
		return strings.Compare(x.Kind, y.Kind)
	})
	if l, ok := s.last[a.ID]; ok {
		rt.LastError, rt.LastErrorAt = Brief(l.err), l.at
	}
	rt.Active = s.active[a.ID]
	return rt
}

func (s *State) Reset(a Account, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, st := range s.scopes {
		if !s.covers(k, &a) {
			continue
		}
		for _, b := range st.blocks {
			if b.until.After(now) {
				n++
			}
		}
		s.drop(k)
	}
	if _, banned := s.bans[a.ID]; !banned {
		delete(s.last, a.ID)
	}
	return n
}

func (s *State) covers(k scopeKey, a *Account) bool {
	if k.kind == scopeEndpoint {
		return k.id == a.endpoint()
	}
	return k.id == a.ID
}

func Brief(e *llm.Error) *llm.Error {
	if e == nil {
		return nil
	}
	c := *e
	c.Raw, c.Usage = nil, nil
	return &c
}
