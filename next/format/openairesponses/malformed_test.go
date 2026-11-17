package openairesponses

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// An error for upstream data that is not JSON keeps no start of a credential at the end of its
// Raw where a cut of the data fell inside the credential: core's redaction, which runs over
// every error an extension sends, cannot recognise a credential's start. Data short enough to
// keep whole is kept.
func TestMalformedKeepsNoCutCredential(t *testing.T) {
	const secret = "sk-malformed-cut-0123456789abcdef"
	r := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: secret})
	// Not JSON, longer than an error keeps, cut 10 bytes into the secret wherever it is cut.
	long := []byte(strings.Repeat("x", 1<<20-10) + secret + strings.Repeat("y", 4096))
	decoders := map[string]func([]byte) error{
		"stream":   func(b []byte) error { _, err := NewStreamDecoder().Decode(b); return err },
		"response": func(b []byte) error { _, err := DecodeResponse(b); return err },
	}
	for name, decode := range decoders {
		e, ok := decode(long).(*llm.Error)
		if !ok {
			t.Fatalf("%s: no *llm.Error for data that is not JSON", name)
		}
		raw := string(r.Event(llm.Event{Type: llm.EventError, Error: e}).Error.Raw)
		if i := strings.Index(raw, secret[:4]); i >= 0 {
			t.Errorf("%s: the error's Raw keeps the start of the credential, redacted: %q", name, raw[max(0, i-20):])
		}
		e, ok = decode([]byte("{nope")).(*llm.Error)
		if !ok || string(e.Raw) != `"{nope"` {
			t.Errorf("%s: short data not kept whole in Raw: %v", name, e)
		}
	}
}
