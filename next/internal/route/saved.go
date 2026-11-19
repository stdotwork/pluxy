package route

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// SavedBlock is a quota block as core saves it across a restart.
type SavedBlock struct {
	Account string `json:"account"`
	// Scope is credential (the whole account) or model (Model on it).
	Scope  string    `json:"scope"`
	Model  string    `json:"model,omitempty"`
	Until  time.Time `json:"until"`
	Source string    `json:"source"`
}

// Changes is signalled, at most once until it is read, when what Saved returns may have changed.
func (s *State) Changes() <-chan struct{} { return s.changes }

// notify signals Changes. It never waits.
func (s *State) notify() {
	select {
	case s.changes <- struct{}{}:
	default:
	}
}

// Saved returns the quota blocks on accounts' credentials and models that last beyond now, for
// core to save so that a restart sends no requests to accounts whose usage ran out.
func (s *State) Saved(now time.Time) []SavedBlock {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []SavedBlock{}
	for k, st := range s.scopes {
		b := st.blocks[kindIndex(blockQuota)]
		if k.kind == scopeEndpoint || !b.until.After(now) {
			continue
		}
		out = append(out, SavedBlock{Account: k.id, Scope: k.kind, Model: k.model, Until: b.until.UTC(), Source: cmp.Or(b.source, SourceBackoff)})
	}
	slices.SortFunc(out, func(x, y SavedBlock) int {
		return cmp.Or(strings.Compare(x.Account, y.Account), strings.Compare(x.Scope, y.Scope), strings.Compare(x.Model, y.Model))
	})
	return out
}

// Load puts back saved quota blocks, as Saved returned them before a restart, for the accounts
// known reports. Blocks that have ended by now are left out, and so is what does not read as a
// block. Each block that would be checked is checked at once (Router.Check), so one that no
// longer holds ends at the first check. A block saved further off than any believed reset is
// held as an unbelieved one.
func (s *State) Load(saved []SavedBlock, now time.Time, known func(id string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sb := range saved {
		if sb.Account == "" || !sb.Until.After(now) || known != nil && !known(sb.Account) {
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
		b := &st.blocks[kindIndex(blockQuota)]
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
		s.set(b, k, blockQuota, until, source, why, now)
		if !b.check.IsZero() {
			b.check = now
		}
		b.reason = &llm.Error{Code: llm.CodeQuota, Status: 429, Scope: k.kind, Message: "a usage window was used up before core restarted"}
	}
	s.notify()
}
