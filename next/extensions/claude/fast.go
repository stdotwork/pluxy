package main

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// fastBeta is the beta Anthropic requires with "speed": "fast" (fast mode); without it Anthropic
// refuses the speed field.
const fastBeta = "fast-mode-2026-02-01"

// isFast reports whether a Messages request asks for fast mode: its body's speed is "fast"
// (letter case and surrounding spaces aside), or its anthropic-beta list names fast mode.
func isFast(body []byte, betas string) bool {
	for _, b := range strings.Split(betas, ",") {
		if strings.TrimSpace(b) == fastBeta {
			return true
		}
	}
	var r struct {
		Speed any `json:"speed"`
	}
	if json.Unmarshal(body, &r) != nil {
		return false
	}
	s, ok := r.Speed.(string)
	return ok && strings.EqualFold(strings.TrimSpace(s), "fast")
}

// fastCredits reports Anthropic's refusal of fast mode to an account without the usage credits
// it needs. Only the request can change that (no other account fares better for it, and the
// account is fine for other requests), so the refusal is the request's. A real rate limit
// never mentions fast mode.
func fastCredits(body []byte) bool {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	m := e.Error.Message
	if m == "" {
		m = string(body)
	}
	m = strings.ToLower(m)
	return strings.Contains(m, "fast request rejected") ||
		(strings.Contains(m, "fast") && (strings.Contains(m, "usage credits") || strings.Contains(m, "credits are required")))
}

// fastFailure scopes the failure of a fast-mode request sent to Anthropic's own API to the
// request, as today's core does: it neither blocks the account (whose other requests may run
// well) nor moves the request on to another account. A refused shared usage window (quota)
// is still the account's.
func fastFailure(e *llm.Error) *llm.Error {
	if e.Code != llm.CodeQuota && e.Code != llm.CodeAccountClosed {
		e.Scope = llm.ScopeRequest
	}
	return e
}
