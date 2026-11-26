package protocol_test

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// A page's URL or title can carry what the upstream put there, so the credential comes out of the
// citations and sources an event holds, and the caller's own lists are left as they were.
func TestRedactorEventCitationsAndSources(t *testing.T) {
	secret := "sk-super-secret-token-1234567890"
	r := protocol.NewRedactor(&protocol.Credential{Kind: "api_key", APIKey: secret})

	end := llm.Event{Type: llm.EventItemEnd, Citations: llm.Citations{{URL: "https://a.example/?k=" + secret, Title: "t " + secret}}}
	fin := llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Sources: llm.Sources{{URL: "https://b.example/?k=" + secret, Title: "t " + secret}}}

	got := r.Event(end)
	if len(got.Citations) != 1 {
		t.Fatalf("citations = %+v, want one", got.Citations)
	}
	got2 := r.Event(fin)
	if len(got2.Sources) != 1 {
		t.Fatalf("sources = %+v, want one", got2.Sources)
	}
	for name, v := range map[string]string{
		"citation url": got.Citations[0].URL, "citation title": got.Citations[0].Title,
		"source url": got2.Sources[0].URL, "source title": got2.Sources[0].Title,
	} {
		if strings.Contains(v, secret) {
			t.Errorf("%s still holds the secret: %q", name, v)
		}
		if !strings.Contains(v, protocol.Redacted) {
			t.Errorf("%s has no redaction marker: %q", name, v)
		}
	}
	if !strings.Contains(end.Citations[0].URL, secret) || !strings.Contains(fin.Sources[0].URL, secret) {
		t.Error("redacting changed the caller's lists")
	}
}
