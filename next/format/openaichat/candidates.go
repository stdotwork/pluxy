package openaichat

import "strings"

// maxChatCandidates is the documented maximum for Chat Completions n
// (https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create).
const maxChatCandidates = 128

// candidateModels are the OpenAI Chat Completions model families known to accept an explicit n.
// The Chat Completions create reference notes: "Newer models reject an explicit n, even n=1."
// Neither the create reference nor the reasoning guide documents n > 1 support for o1, o3,
// o3-mini or o4-mini, so reasoning models, gpt-5*, gpt-6*, audio/realtime/search/codex models,
// and unknown models do not receive a translated n.
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

// supportsCandidateCount reports whether model is a documented OpenAI Chat Completions model that
// accepts an explicit n parameter.
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

// stripDateSuffix removes a trailing -YYYY-MM-DD dated snapshot suffix from a model ID.
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
