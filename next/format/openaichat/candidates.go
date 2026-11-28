package openaichat

import "strings"

const maxChatCandidates = 128

var candidateModels = map[string]struct{}{
	"gpt-3.5-turbo":       {},
	"gpt-3.5-turbo-16k":   {},
	"gpt-4":               {},
	"gpt-4-32k":           {},
	"gpt-4-turbo":         {},
	"gpt-4-turbo-preview": {},
	"gpt-4o":              {},
	"gpt-4o-mini":         {},
	"gpt-4.1":             {},
	"gpt-4.1-mini":        {},
	"gpt-4.1-nano":        {},
	"chatgpt-4o-latest":   {},
}

func supportsCandidateCount(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if _, ok := candidateModels[m]; ok {
		return true
	}
	if base, ok := stripDateSuffix(m); ok {
		_, ok = candidateModels[base]
		return ok
	}
	return false
}

func stripDateSuffix(m string) (string, bool) {
	if len(m) <= 11 || m[len(m)-11] != '-' || m[len(m)-6] != '-' || m[len(m)-3] != '-' {
		return "", false
	}
	for _, i := range []int{10, 9, 8, 7, 5, 4, 2, 1} {
		c := m[len(m)-i]
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return m[:len(m)-11], true
}
