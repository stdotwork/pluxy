package protocol

import (
	"bytes"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// An error's dropped reaches core: it survives a frame's encoding and the redactor, which removes
// the credential from it as from a translation report's loss paths.
func TestErrorDroppedSurvivesTheWire(t *testing.T) {
	ev := llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: "cannot carry parts of this request", Dropped: []string{"response_format", "tools[0].sk-secret-123"}}}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, ev); err != nil {
		t.Fatal(err)
	}
	var got llm.Event
	if err := ReadFrame(&buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == nil || !slices.Equal(got.Error.Dropped, ev.Error.Dropped) {
		t.Fatalf("read back %+v, want dropped %v", got.Error, ev.Error.Dropped)
	}
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-secret-123"})
	out := r.Event(got)
	if want := []string{"response_format", "tools[0].[redacted]"}; !slices.Equal(out.Error.Dropped, want) {
		t.Errorf("redacted dropped = %v, want %v", out.Error.Dropped, want)
	}
	if got.Error.Dropped[1] != "tools[0].sk-secret-123" {
		t.Errorf("the redactor changed the event it was given: %v", got.Error.Dropped)
	}
	if e := r.Event(llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUnsupported}}); e.Error.Dropped != nil {
		t.Errorf("an error without dropped got %v", e.Error.Dropped)
	}
}
