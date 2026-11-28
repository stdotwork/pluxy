package route

import (
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

func (c *call) exhausted(e *llm.Error) *llm.Exhausted {
	if e == nil || e.Code == llm.CodeCancelled || len(c.capable)+len(c.held) == 0 {
		return nil
	}
	r := c.r
	now := r.now()
	down, ext := false, ""
	var back time.Time
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
			return nil
		}
		if soonest.IsZero() || until.Before(soonest) {
			soonest = until
		}
	}
	return &llm.Exhausted{At: now, Until: soonest}
}

func WaitCannotHelp(e *llm.Error) bool {
	if e == nil || e.Exhausted == nil {
		return false
	}
	x := e.Exhausted
	return x.Until.IsZero() || x.Until.Sub(x.At) > usageLimitMinReset
}
