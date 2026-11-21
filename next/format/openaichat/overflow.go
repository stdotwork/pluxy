package openaichat

import (
	"errors"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// CodeContextLengthExceeded is the provider code of an error that says the prompt does not fit the
// model's context window. It is OpenAI's code, and the one the Codex CLI compacts on, with status
// 400 or in a stream.
const CodeContextLengthExceeded = "context_length_exceeded"

// contextOverflowMessages are the upstream messages, in lower case, that say the prompt does not
// fit. They are those LiteLLM's is_error_str_context_window_exceeded recognises, without its
// entry for OpenAI's limit on the length of one string, which compacting does not cure.
var contextOverflowMessages = []string{
	"exceed context limit",
	"this model's maximum context length is",
	"requested token count exceeds the model's maximum context length",
	"model's maximum context limit",
	"is longer than the model's context length",
	"input tokens exceed the configured limit",
	"`inputs` tokens + `max_new_tokens` must be",
	"exceeds the available context size",           // llama.cpp
	"exceeds the maximum number of tokens allowed", // Gemini
	"prompt is too long",                           // Anthropic
}

// isContextOverflow reports whether an upstream's message says the prompt does not fit.
func isContextOverflow(message string) bool {
	m := strings.ToLower(message)
	if strings.Contains(m, "string_above_max_length") || (strings.Contains(m, "invalid 'user'") && strings.Contains(m, "string too long")) {
		return false
	}
	for _, s := range contextOverflowMessages {
		if strings.Contains(m, s) {
			return true
		}
	}
	return strings.Contains(m, "current length is") && strings.Contains(m, "while limit is") ||
		strings.Contains(m, "maximum input length is") && strings.Contains(m, "tokens") ||
		strings.Contains(m, "input too long:") && strings.Contains(m, "input tokens, limit is")
}

// MarkContextOverflow gives err, when it is an upstream's refusal of a prompt too long for the
// model, the code CodeContextLengthExceeded, status 400 and ScopeRequest: it fails this request
// only, and is not tried on another account. Any other error comes back unchanged. An extension
// calls it on the errors of its upstream's replies.
func MarkContextOverflow(err error) error {
	var e *llm.Error
	if !errors.As(err, &e) || (e.Status != 0 && e.Status != http.StatusBadRequest) ||
		(e.Code != llm.CodeInvalidRequest && e.Code != llm.CodeUpstream) {
		return err
	}
	if e.ProviderCode != CodeContextLengthExceeded && !isContextOverflow(e.Message) {
		return err
	}
	e.Code, e.Scope, e.Status, e.Retryable = llm.CodeInvalidRequest, llm.ScopeRequest, http.StatusBadRequest, false
	e.ProviderCode = CodeContextLengthExceeded
	return err
}
