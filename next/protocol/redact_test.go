package protocol

import (
	"bytes"
	"encoding/json"
	"runtime"
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

// A credential made only of digits can sit in a JSON number; the number is replaced by 0, so the
// JSON keeps its shape and the value stays a number, whatever else the data holds.
func TestRedactorRawNumericCredential(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345678"})
	for _, c := range []struct{ in, want string }{
		{`{"n":12345678}`, `{"n":0}`},
		{`{"n":12345678,"s":"\u0061"}`, `{"n":0,"s":"\u0061"}`},
		{`[1,-12345678.5e3,"a"]`, `[1,0,"a"]`},
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

// Review probes of the layered redaction. Expected values were written by hand.
func TestRedactorKeepsWhatIsNotTheCredential(t *testing.T) {
	raw := func(key, in string) string {
		r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: key})
		return string(r.Event(llm.Event{Type: llm.EventNative, Raw: json.RawMessage(in)}).Raw)
	}
	for _, c := range []struct{ name, key, in, want string }{
		// Only the escaped characters that spell the credential are decoded and replaced: the
		// other escapes keep their bytes (\t stays \t, not a tab).
		{"other escapes", "sk-test/key+42",
			`{"m":"failed opening C:\\temp and key \\u0073k-test/key+42"}`,
			`{"m":"failed opening C:\\temp and key [redacted]"}`},
		// A credential in a number is replaced by a number, so typed readers still read it.
		{"number stays a number", "4242",
			`{"usage":{"input_tokens":4242,"output_tokens":7}}`,
			`{"usage":{"input_tokens":0,"output_tokens":7}}`},
		// JSON inside a string stays valid JSON.
		{"number in quoted JSON", "12345678", `{"m":"{\"n\":12345678}"}`, `{"m":"{\"n\":0}"}`},
		// A surrogate pair decodes to one character, as a client reads it.
		{"surrogate pair", "pa😀ss", `{"m":"upstream: pa\\ud83d\\ude00ss"}`, `{"m":"upstream: [redacted]"}`},
	} {
		if got := raw(c.key, c.in); got != c.want {
			t.Errorf("%s: raw %s\n got %s\nwant %s", c.name, c.in, got, c.want)
		}
	}
	var typed struct {
		Usage struct{ InputTokens, OutputTokens int } `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw("4242", `{"usage":{"input_tokens":4242,"output_tokens":7}}`)), &typed); err != nil {
		t.Errorf("redacted usage no longer reads as typed usage: %v", err)
	}
}

// Percent-encoding in any case, and encoded twice, as a URL quoted in a URL is.
func TestRedactorPercentEncodings(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"})
	for _, c := range []struct{ in, want string }{
		{"key=sk-test%2fkey%2b42", "key=[redacted]"},
		{"key=sk-test%252Fkey%252B42", "key=[redacted]"},
		{"key=sk-test%252fkey%252b42&next=%2Fhome", "key=[redacted]&next=%2Fhome"},
	} {
		if got := r.String(c.in); got != c.want {
			t.Errorf("String(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Data without the credential comes back with the same bytes, escapes and all.
func TestRedactorLeavesOtherDataAlone(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"})
	for _, in := range []string{
		`{"a":"C:\\temp\t\u00e9 \ud83d\ude00 \/ %2F%2b","n":[42,-1.5e3],"s":"sk-test/key+4"}`,
		`{"m":"{\"key\":\"\\u0073k-test/key+41\"}"}`,
		`"sk-test%2Fkey%2B43"`,
	} {
		ev := r.Event(llm.Event{Type: llm.EventNative, Raw: json.RawMessage(in)})
		if string(ev.Raw) != in {
			t.Errorf("raw %s\n got %s", in, ev.Raw)
		}
		if got := r.String(in); got != in {
			t.Errorf("String(%s) = %s", in, got)
		}
	}
}

// Redacting a large reply that does not hold the credential neither copies nor decodes it.
func TestRedactorLargeReplyWithoutCredentialIsCheap(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"})
	for name, body := range map[string][]byte{
		"plain":   largeBody(`{"type":"text","text":"Hello there, a long reply.","n":12345}`),
		"escapes": largeBody(`{"type":"text","text":"line\none \"quoted\" caf\u00e9\ttab","n":-1.5e3}`),
	} {
		var got json.RawMessage
		if n := allocated(func() { got = r.raw(body) }); n > uint64(len(body))/16 {
			t.Errorf("%s: redacting a %d-byte body allocated %d bytes", name, len(body), n)
		}
		if string(got) != string(body) {
			t.Fatalf("%s: body changed", name)
		}
		text := string(body)
		if n := allocated(func() { _ = r.String(text) }); n > uint64(len(body))/16 {
			t.Errorf("%s: redacting %d bytes of text allocated %d bytes", name, len(body), n)
		}
	}
}

// allocated returns how many bytes f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// largeBody returns a JSON array of item, repeated to about 4 MiB.
func largeBody(item string) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for b.Len() < 4<<20 {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.WriteString(item)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// BenchmarkRedactorLargeReply redacts about 4 MiB of reply data that does not hold the
// credential: plain JSON, JSON with ordinary escapes, and JSON with escaped backslashes (code).
func BenchmarkRedactorLargeReply(b *testing.B) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"})
	for _, c := range []struct{ name, item string }{
		{"plain", `{"type":"text","text":"Hello there, a long reply.","n":12345}`},
		{"escapes", `{"type":"text","text":"line\none \"quoted\" caf\u00e9\ttab","n":-1.5e3}`},
		{"backslashes", `{"type":"text","text":"printf(\"%d\\n\", x); // C:\\temp"}`},
	} {
		body := largeBody(c.item)
		b.Run("raw/"+c.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for range b.N {
				_ = r.raw(body)
			}
		})
		text := string(body)
		b.Run("string/"+c.name, func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			for range b.N {
				_ = r.String(text)
			}
		})
	}
}

// Second-review probes. Expected values were written by hand.
func TestRedactorReviewRound2(t *testing.T) {
	raw := func(key, in string) string {
		r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: key})
		return string(r.Event(llm.Event{Type: llm.EventNative, Raw: json.RawMessage(in)}).Raw)
	}
	for _, c := range []struct{ name, key, in, want string }{
		// A key holding what reads as a percent escape, behind a JSON escape: decoding both at
		// once would turn %41 into A and miss it.
		{"json escape, then a percent sign in the key", "sk-%41tail", `{"m":"upstream: \\u0073k-%41tail"}`, `{"m":"upstream: [redacted]"}`},
		// The low surrogate appears only once %5C is decoded.
		{"percent, then a surrogate pair", "pa😀ss", `{"m":"upstream: pa\\ud83d%5Cude00ss"}`, `{"m":"upstream: [redacted]"}`},
		// Escapes away from the key keep their bytes in a string that changes.
		{"other escapes in a changed string", "sk-test/key+42", `{"m":"caf\u00e9 \/home key \u0073k-test/key+42 \t done"}`, `{"m":"caf\u00e9 \/home key [redacted] \t done"}`},
		// JSON after other text in a string stays valid JSON.
		{"number in JSON after text", "12345678", `{"m":"upstream: {\"n\":12345678,\"m\":[12345678]}"}`, `{"m":"upstream: {\"n\":0,\"m\":[0]}"}`},
		// Go's JSON decoder reads an invalid byte as U+FFFD, so for a key holding U+FFFD the byte
		// spells it.
		{"invalid UTF-8", "pa�ss", "{\"m\":\"key pa\xffss\"}", `{"m":"key [redacted]"}`},
	} {
		if got := raw(c.key, c.in); got != c.want {
			t.Errorf("%s: raw %s\n got %s\nwant %s", c.name, c.in, got, c.want)
		}
	}
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345678"})
	for in, want := range map[string]string{
		`upstream: {"n":12345678}`: `upstream: {"n":0}`,
		"key 12345678 refused":     "key [redacted] refused",
	} {
		if got := r.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

// Each line of a secret that spans lines (a PEM private key) counts as secret on its own, so text
// split into lines, such as a log, cannot show it a line at a time.
func TestRedactorMultilineSecret(t *testing.T) {
	key := "-----BEGIN PRIVATE KEY-----\nbm90LWEtcmVhbC1rZXktb25seS1wcm9iZQ==\n-----END PRIVATE KEY-----\n"
	data, _ := json.Marshal(map[string]string{"private_key": key})
	r := NewRedactor(&Credential{Kind: "service_account", Data: data})
	for in, want := range map[string]string{
		"bm90LWEtcmVhbC1rZXktb25seS1wcm9iZQ==": "[redacted]",
		"key: " + key:                          "key: [redacted]",
		"short line\nok":                       "short line\nok",
	} {
		if got := r.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}
