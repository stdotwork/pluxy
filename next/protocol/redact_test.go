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

// A reasoning item's opaque data reaches clients (a Responses client gets it as
// encrypted_content), so an upstream that echoes the credential there has it removed too.
func TestRedactorOpaque(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialOAuth, AccessToken: "oauth-access-secret"})
	in := &llm.Opaque{Format: llm.FormatAnthropicMessages, Data: "Esig-oauth-access-secret"}
	ev := r.Event(llm.Event{Type: llm.EventItemEnd, Opaque: in})
	if ev.Opaque == nil || ev.Opaque.Data != "Esig-[redacted]" || ev.Opaque.Format != llm.FormatAnthropicMessages {
		t.Errorf("opaque = %+v, want the access token redacted", ev.Opaque)
	}
	if in.Data != "Esig-oauth-access-secret" {
		t.Error("redaction changed the caller's event")
	}
}

// Raw upstream data reaches clients as JSON, and a client decodes JSON escapes, so the
// credential must go wherever a decoded string holds it, whichever characters the upstream
// escaped: the reviewer's upstream escaped only the first one. A string holding JSON is decoded
// in turn, as a client parsing it would. Expected values were written by hand.
func TestRedactorRawDecodesEscapes(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"})
	for _, c := range []struct{ in, want string }{
		{`{"message":"Rejected \u0073k-test/key+42"}`, `{"message":"Rejected [redacted]"}`},
		{`{"message":"Rejected sk-test\/key\u002B42","code":"x"}`, `{"message":"Rejected [redacted]","code":"x"}`},
		{`{"message":"Rejected sk-test\/key\u002b42"}`, `{"message":"Rejected [redacted]"}`},
		{`{"m":"\u0073\u006B\u002d\u0074\u0065\u0073\u0074\u002F\u006b\u0065\u0079\u002B\u0034\u0032"}`, `{"m":"[redacted]"}`},
		{`{"\u0073k-test/key+42":1}`, `{"[redacted]":1}`},
		{`["a \"quoted\" \u0073k-test/key+42",2]`, `["a \"quoted\" [redacted]",2]`},
		{`"Rejected \u0073k-test/key+42"`, `"Rejected [redacted]"`},
		{`{"error":{"message":"{\"key\":\"\\u0073k-test/key+42\"}"}}`, `{"error":{"message":"{\"key\":\"[redacted]\"}"}}`},
		// Data around the credential keeps its bytes, escapes included.
		{`{"a":"caf\u00e9 <b>","b":[1, 2],"c":"\u0073k-test/key+42"}`, `{"a":"caf\u00e9 <b>","b":[1, 2],"c":"[redacted]"}`},
		// A string holding a JSON string, as a client decoding twice would read it.
		{`{"system_fingerprint":"\"\\u0073k-test/key+42\""}`, `{"system_fingerprint":"\"[redacted]\""}`},
		// Every character escaped twice, with no JSON structure to decode it by.
		{`{"m":"x \\u0073\\u006B-test\\/key+42"}`, `{"m":"x [redacted]"}`},
		// JSON after other text, as in an error message quoting a body.
		{`{"m":"upstream said: {\"key\":\"\\u0073k-test/key+42\"}"}`, `{"m":"upstream said: {\"key\":\"[redacted]\"}"}`},
		// The encodings String removes are removed inside a decoded string too.
		{`{"m":"token c2stdGVzdC9rZXkrNDI\u003d refused"}`, `{"m":"token [redacted] refused"}`},
		{`{"m":"key=sk-test%2Fkey%2B42"}`, `{"m":"key=[redacted]"}`},
	} {
		ev := r.Event(llm.Event{Type: llm.EventNative, Raw: json.RawMessage(c.in)})
		if got := string(ev.Raw); got != c.want {
			t.Errorf("raw %s\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// A credential made only of digits can sit in a JSON number; the number is replaced by the
// redacted string and the JSON keeps its shape, whatever else the data holds.
func TestRedactorRawNumericCredential(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345678"})
	for _, c := range []struct{ in, want string }{
		{`{"n":12345678}`, `{"n":"[redacted]"}`},
		{`{"n":12345678,"s":"\u0061"}`, `{"n":"[redacted]","s":"\u0061"}`},
		{`[1,-12345678.5e3,"a"]`, `[1,"[redacted]","a"]`},
		{`{"n":1234567}`, `{"n":1234567}`},
	} {
		ev := r.Event(llm.Event{Type: llm.EventNative, Raw: json.RawMessage(c.in)})
		if got := string(ev.Raw); got != c.want {
			t.Errorf("raw %s\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// Error text is decoded the same way: an upstream message can quote a JSON body that holds the
// credential escaped.
func TestRedactorErrorTextDecodesEscapes(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"})
	ev := r.Event(llm.Event{Type: llm.EventError, Error: &llm.Error{
		Message:      `Rejected {"key":"\u0073k-test/key+42"}`,
		ProviderCode: `"\u0073k-test/key+42"`,
	}})
	if ev.Error.Message != `Rejected {"key":"[redacted]"}` || ev.Error.ProviderCode != `"[redacted]"` {
		t.Errorf("message %q, provider code %q", ev.Error.Message, ev.Error.ProviderCode)
	}
}
