package llm

import (
	"errors"
	"testing"
)

func TestMarkContextOverflow(t *testing.T) {
	for _, msg := range []string{
		"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).",
		"This model's maximum context length is 8192 tokens.",
		"prompt is too long: 210000 tokens > 200000 maximum",
		"the request exceeds the available context size",
		"Current length is 9000 while limit is 8192",
		"Input too long: 9000 input tokens, limit is 8192",
	} {
		e := &Error{Code: CodeUpstream, Scope: ScopeEndpoint, Message: msg, Retryable: true}
		_ = MarkContextOverflow(e)
		if e.Code != CodeInvalidRequest || e.Scope != ScopeRequest || e.Status != 400 || e.ProviderCode != ProviderContextLengthExceeded || e.Retryable {
			t.Errorf("%q: %+v, want an overflow on this request only", msg, e)
		}
	}
	coded := &Error{Code: CodeUpstream, ProviderCode: ProviderContextLengthExceeded, Message: "too big"}
	if _ = MarkContextOverflow(coded); coded.Scope != ScopeRequest || coded.Status != 400 {
		t.Errorf("an error with the code = %+v", coded)
	}
	for name, e := range map[string]*Error{
		"other 400":       {Code: CodeInvalidRequest, Status: 400, ProviderCode: "x", Scope: ScopeRequest, Message: "max_tokens is too large"},
		"one long string": {Code: CodeInvalidRequest, Status: 400, ProviderCode: "string_above_max_length", Scope: ScopeRequest, Message: "string_above_max_length: this model's maximum context length is"},
		"a 429":           {Code: CodeRateLimit, Status: 429, ProviderCode: "x", Scope: ScopeModel, Message: "prompt is too long"},
		"a 401":           {Code: CodeAuth, Status: 401, ProviderCode: "x", Scope: ScopeCredential, Message: "prompt is too long"},
		"a 500":           {Code: CodeUpstream, Status: 500, ProviderCode: "x", Scope: ScopeEndpoint, Message: "prompt is too long"},
	} {
		want := *e
		_ = MarkContextOverflow(e)
		if e.Code != want.Code || e.Scope != want.Scope || e.Status != want.Status || e.ProviderCode != want.ProviderCode {
			t.Errorf("%s changed: %+v", name, e)
		}
	}
	plain := errors.New("prompt is too long")
	if got := MarkContextOverflow(plain); got != plain {
		t.Errorf("a plain error = %v", got)
	}
}
