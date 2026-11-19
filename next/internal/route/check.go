package route

import (
	"context"
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Readings reads an account's usage from its provider: core's quota call to the account's
// extension.
type Readings interface {
	// Reading returns account a's usage windows as its provider reports them. An error with code
	// unsupported means the account has no reading (an API key, or an extension without one);
	// any other error, that the reading could not be made now.
	Reading(ctx context.Context, a Account) (*protocol.Quota, *llm.Error)
}

// UseReadings sets where Check reads accounts' usage from. Without one, every check lets one
// request through instead.
func (r *Router) UseReadings(rd Readings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readings = rd
}

var noReading = &llm.Error{Code: llm.CodeUnsupported, Message: "no usage reading"}

// Check runs the checks that are due on the router's accounts. A rate limit or quota block on an
// account's credential or one of its models that has more than the check interval left is
// checked once per interval (see State.SetCheckInterval), and a block whose end was not believed
// or that was read back at start is checked too. The check is the provider's own usage reading,
// where the account has one: it ends the block when no usage window that covers the block is
// full, and otherwise moves the block's end to the reset of the covering full window that resets
// last, sooner or later. A window covers a block on the account's credential when it counts the
// whole account (it names no models and is not a feature's), and a block on one model when it
// counts the whole account or names the model; a feature's window covers no block. Where
// the account has no reading, the check lets the next request through instead, and that
// request's outcome is the answer. A reading that cannot be made leaves the block; it is tried
// again an interval on. Only accounts the router routes over are checked: a block on one that is
// turned off or waits for a sign-in stays due until it routes again. Readings are made one at a
// time; core calls Check on a timer.
func (r *Router) Check(ctx context.Context) {
	accounts, _ := r.snapshot()
	r.mu.RLock()
	rd := r.readings
	r.mu.RUnlock()
	routes := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		routes[a.ID] = true
	}
	due := r.state.due(r.now(), routes)
	for _, a := range accounts {
		claims := due[a.ID]
		if len(claims) == 0 {
			continue
		}
		q, e := (*protocol.Quota)(nil), noReading
		if rd != nil {
			q, e = rd.Reading(ctx, *a)
		}
		r.state.checked(claims, q, e, r.now())
	}
}

// nextCheck returns when a block that ends at until is next checked after a check at now: an
// interval on when more than an interval is left, and never otherwise (the zero time), so no
// check is due after the block has ended.
func (s *State) nextCheck(until, now time.Time) time.Time {
	if until.Sub(now) > s.checkEvery {
		return now.Add(s.checkEvery)
	}
	return time.Time{}
}

// claim is one check taken on: block kind index i on scope key, as it was when taken.
type claim struct {
	key   scopeKey
	i     int
	mark  uint64
	until time.Time
}

// due takes on the checks due at now on the accounts in routes, by account ID: each one's next
// check moves an interval on when more than an interval is left (nextCheck), so a check that
// never answers is made again then. A block on an account that does not route now (turned off,
// or waiting for a sign-in) is left due, to be checked once it routes again.
func (s *State) due(now time.Time, routes map[string]bool) map[string][]claim {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]claim{}
	for k, st := range s.scopes {
		if k.kind == scopeEndpoint || !routes[k.id] {
			continue
		}
		for i := range st.blocks {
			b := &st.blocks[i]
			if b.check.IsZero() || b.check.After(now) || b.pass || !b.until.After(now) {
				continue
			}
			b.check = s.nextCheck(b.until, now)
			out[k.id] = append(out[k.id], claim{key: k, i: i, mark: b.mark, until: b.until})
		}
	}
	return out
}

// checked applies a check's answer, the reading q or the error e, to the blocks claimed. A block
// that an outcome changed since its check was taken on is left as that outcome set it.
func (s *State) checked(claims []claim, q *protocol.Quota, e *llm.Error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range claims {
		st := s.scopes[c.key]
		if st == nil {
			continue
		}
		b := &st.blocks[c.i]
		if b.mark != c.mark || !b.until.Equal(c.until) || !b.until.After(now) {
			continue
		}
		switch {
		case e != nil && e.Code == llm.CodeUnsupported:
			b.pass, b.check = true, time.Time{} // no reading: the next request is the check
		case e != nil || q == nil:
			// The reading could not be made: the block holds, with the next check due set when
			// the check was taken on.
		default:
			reset, window, full := fullWindow(q, c.key, now)
			if !full {
				// The block ends, and its backoff starts over, as after a success.
				*b = block{mark: b.mark, reason: b.reason}
				break
			}
			d, source := s.resetFor(reset.Sub(now).Milliseconds())
			why := ""
			if source == SourceUnverified {
				why = unverifiedWhy
			}
			s.set(b, c.key, blockKinds[c.i], now.Add(d), source, why, now)
			b.window = window
		}
		if c.i == kindIndex(blockQuota) {
			s.notify()
		}
	}
}

// fullWindow returns the reset of the full usage window in q that resets last among those that
// cover scope k, and its length in minutes; full is false when none is full. A usage window is a
// limit with a reset time to come; it is full when it is used to 100% or nothing of it remains.
// A window that names models covers a block on one of them, never the whole account; one that
// counts a feature of the plan covers no block; any other covers every block on the account. A
// balance or a count without a reset time is never what holds an account.
func fullWindow(q *protocol.Quota, k scopeKey, now time.Time) (reset time.Time, window int64, full bool) {
	for _, l := range q.Limits {
		if l.Feature || l.Unlimited || l.ResetsAt <= now.Unix() {
			continue
		}
		if len(l.Models) > 0 && (k.kind != scopeModel || !slices.Contains(l.Models, k.model)) {
			continue
		}
		if !(l.UsedPercent != nil && *l.UsedPercent >= 100 || l.Remaining != nil && *l.Remaining <= 0) {
			continue
		}
		if t := time.Unix(l.ResetsAt, 0); t.After(reset) {
			reset, window, full = t, l.WindowSeconds/60, true
		}
	}
	return reset, window, full
}
