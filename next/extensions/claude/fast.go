package main

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// fastBeta is the beta Anthropic requires with "speed": "fast" (fast mode); without it Anthropic
// refuses the speed field.
const fastBeta = "fast-mode-2026-02-01"

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

func fastFailure(e *llm.Error) *llm.Error {
	if e.Code != llm.CodeQuota && e.Code != llm.CodeAccountClosed {
		e.Scope = llm.ScopeRequest
	}
	return e
}
