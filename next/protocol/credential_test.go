package protocol

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// Every secret field of an OAuth credential, including provider data core does not read, is
// removed from what a call sends back, without the redactor listing the fields.
func TestRedactorCoversOAuthCredential(t *testing.T) {
	c := &Credential{Kind: CredentialOAuth, AccessToken: "at-0123456789abcdef", RefreshToken: "rt-0123456789abcdef",
		ExpiresAt: 1767225600, Data: []byte(`{"id_token":"idt-0123456789abcdef","nested":{"key":"sa-private-0123456789"}}`)}
	ev := llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeAuth,
		Message: "refused at-0123456789abcdef / rt-0123456789abcdef / idt-0123456789abcdef / sa-private-0123456789 until 1767225600"}}
	got := NewRedactor(c).Event(ev).Error.Message
	for _, secret := range []string{"at-0123456789abcdef", "rt-0123456789abcdef", "idt-0123456789abcdef", "sa-private-0123456789"} {
		if strings.Contains(got, secret) {
			t.Errorf("message still holds %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "1767225600") {
		t.Errorf("the expiry, which is not secret, was removed: %s", got)
	}
}
