package host

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestOutputMediaMinorGating(t *testing.T) {
	if OutputMediaMinor != 33 {
		t.Fatalf("OutputMediaMinor = %d, want 33", OutputMediaMinor)
	}
	if protocol.Minor < OutputMediaMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, OutputMediaMinor)
	}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	ev := llm.Event{
		Type:  llm.EventMedia,
		Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: png},
		Raw:   json.RawMessage(`{"candidates":[]}`),
	}

	oldExt := &Extension{info: protocol.Info{ProtocolMinor: OutputMediaMinor - 1}}
	gotOld := oldExt.normalizeEvent(ev)
	if gotOld.Media != nil {
		t.Errorf("minor %d kept Media: %+v, want nil", OutputMediaMinor-1, gotOld.Media)
	}
	if gotOld.Type != llm.EventNative {
		t.Errorf("minor %d with Raw: type = %q, want %q", OutputMediaMinor-1, gotOld.Type, llm.EventNative)
	}

	evNoRaw := llm.Event{
		Type:  llm.EventMedia,
		Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: png},
	}
	gotOldNoRaw := oldExt.normalizeEvent(evNoRaw)
	if gotOldNoRaw.Media != nil {
		t.Errorf("minor %d (no raw) kept Media: %+v, want nil", OutputMediaMinor-1, gotOldNoRaw.Media)
	}

	newExt := &Extension{info: protocol.Info{ProtocolMinor: OutputMediaMinor}}
	gotNew := newExt.normalizeEvent(ev)
	if gotNew.Type != llm.EventMedia || gotNew.Media == nil || gotNew.Media.Data != png {
		t.Errorf("minor %d Media = %+v, want preserved", OutputMediaMinor, gotNew.Media)
	}
}
