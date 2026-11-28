package route

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
)

type SavedBan struct {
	Account string    `json:"account"`
	Why     string    `json:"why,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

type SavedBlock struct {
	Account string      `json:"account"`
	Scope   string      `json:"scope"`
	Model   string      `json:"model,omitempty"`
	Kind    string      `json:"kind"`
	Until   time.Time   `json:"until"`
	Source  string      `json:"source"`
	Error   *SavedError `json:"error,omitempty"`
}

type SavedError struct {
	Code    string     `json:"code"`
	Status  int        `json:"status,omitempty"`
	Message string     `json:"message,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

func (s *State) Changes() <-chan struct{} { return s.changes }

func (s *State) notify() {
	select {
	case s.changes <- struct{}{}:
	default:
	}
}

const minSaved = 30 * time.Second

const maxSavedMessage = 256

const restartedWhy = "a usage window was used up before pluxy restarted"

func (s *State) Saved(now time.Time) []SavedBlock {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []SavedBlock{}
	for k, st := range s.scopes {
		if k.kind == scopeEndpoint {
			continue
		}
		last := s.last[k.id]
		for i := range st.blocks {
			b := &st.blocks[i]
			if b.until.Sub(now) <= minSaved {
				continue
			}
			sb := SavedBlock{Account: k.id, Scope: k.kind, Model: k.model, Kind: blockKinds[i], Until: b.until.UTC(),
				Source: cmp.Or(b.source, SourceBackoff), Error: savedError(b.reason)}
			if sb.Error != nil && last.err != nil && last.err == b.reason {
				at := last.at.UTC()
				sb.Error.At = &at
			}
			out = append(out, sb)
		}
	}
	slices.SortFunc(out, func(x, y SavedBlock) int {
		return cmp.Or(strings.Compare(x.Account, y.Account), strings.Compare(x.Scope, y.Scope), strings.Compare(x.Model, y.Model),
			strings.Compare(x.Kind, y.Kind))
	})
	return out
}

func savedError(e *llm.Error) *SavedError {
	if e == nil {
		return nil
	}
	msg := e.Message
	if len(msg) > maxSavedMessage {
		n := maxSavedMessage
		for n > 0 && !utf8.RuneStart(msg[n]) {
			n--
		}
		msg = msg[:n]
	}
	return &SavedError{Code: e.Code, Status: e.Status, Message: msg}
}

func (s *State) Load(saved []SavedBlock, now time.Time, known func(id string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sb := range saved {
		if sb.Account == "" || !sb.Until.After(now) || known != nil && !known(sb.Account) {
			continue
		}
		kind := cmp.Or(sb.Kind, blockQuota)
		if kind != blockQuota && kind != blockRateLimit && kind != blockCooldown {
			continue
		}
		var k scopeKey
		switch {
		case sb.Scope == scopeCredential:
			k = scopeKey{kind: scopeCredential, id: sb.Account}
		case sb.Scope == scopeModel && sb.Model != "":
			k = scopeKey{kind: scopeModel, id: sb.Account, model: sb.Model}
		default:
			continue
		}
		st := s.scopes[k]
		if st == nil {
			if k.kind == scopeModel && s.models >= maxModelKeys {
				continue
			}
			st = &scopeState{}
			s.scopes[k] = st
			if k.kind == scopeModel {
				s.models++
			}
		}
		b := &st.blocks[kindIndex(kind)]
		until, source, why := sb.Until, sb.Source, ""
		switch source {
		case SourceProvider, SourceBackoff, SourceUnverified:
		default:
			source = SourceBackoff
		}
		if until.Sub(now) > maxReset {
			until, source = now.Add(2*s.checkEvery), SourceUnverified
		}
		if source == SourceUnverified {
			why = unverifiedWhy
		}
		if !until.After(b.until) {
			continue
		}
		s.set(b, k, kind, until, source, why, now)
		if kind == blockQuota && !b.check.IsZero() {
			b.check = now
		}
		switch e := sb.Error; {
		case e != nil:
			b.reason = &llm.Error{Code: e.Code, Status: e.Status, Scope: k.kind, Message: e.Message}
			if l, ok := s.last[sb.Account]; e.At != nil && (!ok || e.At.After(l.at)) {
				s.last[sb.Account] = lastFailure{at: *e.At, err: b.reason}
			}
		case kind == blockQuota:
			b.reason = &llm.Error{Code: llm.CodeQuota, Status: 429, Scope: k.kind, Message: restartedWhy}
		}
	}
	s.notify()
}

func (s *State) SavedBans() []SavedBan {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bans) == 0 {
		return nil
	}
	out := make([]SavedBan, 0, len(s.bans))
	for id, ban := range s.bans {
		out = append(out, SavedBan{Account: id, Why: ban.why, At: ban.at.UTC()})
	}
	slices.SortFunc(out, func(x, y SavedBan) int { return strings.Compare(x.Account, y.Account) })
	return out
}

func (s *State) LoadBans(saved []SavedBan, known func(id string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, sb := range saved {
		if sb.Account == "" || known != nil && !known(sb.Account) {
			continue
		}
		why := strings.TrimSpace(sb.Why)
		if why == "" {
			why = "the provider closed or suspended the account"
		}
		s.bans[sb.Account] = banState{
			why:    why,
			at:     sb.At,
			reason: &llm.Error{Code: llm.CodeAccountClosed, Scope: llm.ScopeCredential, Message: why},
		}
		for k := range s.scopes {
			if k.kind != scopeEndpoint && k.id == sb.Account {
				s.drop(k)
			}
		}
		changed = true
	}
	if changed {
		s.notify()
	}
}
