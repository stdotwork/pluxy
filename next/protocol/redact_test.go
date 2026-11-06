package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// The credential is removed in the forms it takes in error text: as is, escaped in JSON or a
// URL, and base64 alone or inside HTTP Basic credentials. Expected values were worked out by
// hand from the encodings.
func TestRedactorEncodings(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"})
	for _, c := range []struct{ in, want string }{
		{"Rejected Bearer sk-test/key+42", "Rejected Bearer [redacted]"},
		{`{"message":"key sk-test\/key+42"}`, `{"message":"key [redacted]"}`},
		{"Post https://h/v1?key=sk-test%2Fkey%2B42&x=1", "Post https://h/v1?key=[redacted]&x=1"},
		{"GET /keys/sk-test%2Fkey+42", "GET /keys/[redacted]"},
		{"token c2stdGVzdC9rZXkrNDI= refused", "token [redacted] refused"},
		// base64("user:sk-test/key+42"): the groups ay10 ZXN0 L2tl eSs0 and the last
		// character of cjpz hold only key bytes.
		{"Basic dXNlcjpzay10ZXN0L2tleSs0Mg==", "Basic dXNlcjp[redacted]Mg=="},
		{"api_key kind is fine", "api_key kind is fine"},
	} {
		if got := r.String(c.in); got != c.want {
			t.Errorf("String(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactorEvent(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-secret-123"})
	ev := r.Event(llm.Event{Type: llm.EventError, Error: &llm.Error{
		Code:         llm.CodeAuth,
		Message:      "bad key sk-secret-123",
		ProviderCode: "sk-secret-123",
		Raw:          json.RawMessage(`{"error":{"message":"bad key sk-secret-123"}}`),
	}})
	e := ev.Error
	if e.Message != "bad key [redacted]" || e.ProviderCode != "[redacted]" || string(e.Raw) != `{"error":{"message":"bad key [redacted]"}}` {
		t.Errorf("error = %+v, raw %s", e, e.Raw)
	}
	if NewRedactor(nil).String("sk-secret-123") != "sk-secret-123" {
		t.Error("a call without a credential changed text")
	}
}
