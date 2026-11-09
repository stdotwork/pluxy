package host

import (
	"context"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// An extension that does not list the endpoint operation at minor version 7 or later answers an
// endpoint call at once, unsupported about the account and retryable, so routing moves on.
func TestEndpointNotServed(t *testing.T) {
	req := llm.Request{Model: "m", Native: &llm.Native{Format: llm.FormatOpenAIResponsesCompact}}
	for _, info := range []protocol.Info{
		{ProtocolMinor: 6, Operations: []string{protocol.OpGenerate, protocol.OpEndpoint}},
		{ProtocolMinor: 7, Operations: []string{protocol.OpGenerate}},
	} {
		if ServesEndpoints(info) {
			t.Fatalf("ServesEndpoints(%+v) = true", info)
		}
		e := &Extension{info: info, manifest: &Manifest{ID: "test/ext"}}
		ev := e.Endpoint(context.Background(), protocol.Account{ID: "a"}, req).Next()
		if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || ev.Error.Scope != llm.ScopeCredential || !ev.Error.Retryable {
			t.Fatalf("%+v: event = %+v", info, ev)
		}
	}
	if !ServesEndpoints(protocol.Info{ProtocolMinor: 7, Operations: []string{protocol.OpEndpoint}}) {
		t.Fatal("ServesEndpoints = false for an extension that lists endpoint at minor 7")
	}
}
