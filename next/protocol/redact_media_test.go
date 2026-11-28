package protocol_test

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestRedactorEventOutputMedia(t *testing.T) {
	secret := "sk-super-secret-token-1234567890"
	r := protocol.NewRedactor(&protocol.Credential{Kind: "api_key", APIKey: secret})

	orig := llm.Event{
		Type: llm.EventMedia,
		Media: &llm.OutputMedia{
			Kind:      "kind-" + secret,
			MediaType: "image/" + secret,
			Data:      "AAAA" + secret + "BBBB",
		},
	}
	red := r.Event(orig)
	if red.Media == nil {
		t.Fatalf("redacted media is nil")
	}
	for name, val := range map[string]string{
		"Kind":      red.Media.Kind,
		"MediaType": red.Media.MediaType,
		"Data":      red.Media.Data,
	} {
		if strings.Contains(val, secret) {
			t.Errorf("%s still contains secret: %q", name, val)
		}
		if !strings.Contains(val, protocol.Redacted) {
			t.Errorf("%s missing redaction marker: %q", name, val)
		}
	}
	if !strings.Contains(orig.Media.Data, secret) {
		t.Errorf("redacting mutated caller's OutputMedia")
	}

	known := r.Event(llm.Event{
		Type:  llm.EventMedia,
		Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: "AAAA"},
	})
	if known.Media.Kind != llm.MediaImage {
		t.Errorf("known kind = %q, want %q", known.Media.Kind, llm.MediaImage)
	}
}
