package route

import (
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// exhausted returns the mark for e, the error that ends the call before a reply began, when no
// account that serves the request's model can take a request now: when the soonest of them can,
// or the zero time when none will until a person acts. It returns nil when one can take a request
// now (a client may try again, as before), when no account serves the model at all, and for a
// cancelled request.
//
// A held account (turned off, or waiting for a new sign-in) and a banned one never come back on
// their own. Nor does an account whose extension does not run: such a refusal
// (extension_unavailable) ends the request at once, on the first account it meets, and says the
// same of every account of that extension, unless it says when the extension starts again
// (RetryAfterMS, after a crash).
func (c *call) exhausted(e *llm.Error) *llm.Exhausted {
	if e == nil || e.Code == llm.CodeCancelled || len(c.capable)+len(c.held) == 0 {
		return nil
	}
	r := c.r
	now := r.now()
	down, ext := false, ""
	var back time.Time // when the extension that does not run starts again, when e says
	if e.Code == llm.CodeExtensionUnavailable && len(c.tried) > 0 {
		down, ext = true, c.tried[len(c.tried)-1].Provider
		if e.RetryAfterMS > 0 {
			back = now.Add(time.Duration(e.RetryAfterMS) * time.Millisecond)
		}
	}
	var soonest time.Time
	for _, t := range c.capable {
		stopped := down && t.a.Extension == ext
		if r.state.IsBanned(t.a.ID) || stopped && back.IsZero() {
			continue
		}
		until, _, _ := r.state.availability(t.a, t.model, now)
		if stopped && back.After(until) {
			until = back
		}
		if !until.After(now) {
			return nil // free now, or only at its concurrency limit
		}
		if soonest.IsZero() || until.Before(soonest) {
			soonest = until
		}
	}
	return &llm.Exhausted{At: now, Until: soonest}
}

// WaitCannotHelp reports whether e ends a request that no account can serve for longer than
// usageLimitMinReset from when routing found it, or until a person acts. A client should then stop
// rather than retry: the usage-limit reply's threshold, so a long or endless wait stops a client
// at once and a short one (a brief overload) is still retried.
func WaitCannotHelp(e *llm.Error) bool {
	if e == nil || e.Exhausted == nil {
		return false
	}
	x := e.Exhausted
	return x.Until.IsZero() || x.Until.Sub(x.At) > usageLimitMinReset
}
