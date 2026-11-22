package conformance

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformanceOutputMedia(t *testing.T) {
	if protocol.Minor < host.OutputMediaMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, host.OutputMediaMinor)
	}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF\x24\x00\x00\x00WAVEfmt "))
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))

	valid := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}},
		{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: png}},
		{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaAudio, MediaType: "audio/wav", Data: wav}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	if err := checkReplyOutputMedia(valid); err != nil {
		t.Fatalf("checkReplyOutputMedia(valid) = %v, want nil", err)
	}

	invalidCases := []struct {
		name string
		evs  []llm.Event
	}{
		{
			name: "media on finish",
			evs: []llm.Event{
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: png}},
			},
		},
		{
			name: "media event with nil Media",
			evs: []llm.Event{
				{Type: llm.EventMedia},
			},
		},
		{
			name: "svg image",
			evs: []llm.Event{
				{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/svg+xml", Data: svg}},
			},
		},
		{
			name: "invalid base64",
			evs: []llm.Event{
				{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: "!!!"}},
			},
		},
		{
			name: "kind mismatch",
			evs: []llm.Event{
				{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaAudio, MediaType: "image/png", Data: png}},
			},
		},
		{
			name: "single part past limit",
			evs: []llm.Event{
				{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: strings.Repeat("A", llm.MaxOutputMediaPartBytes+4)}},
			},
		},
	}
	for _, tc := range invalidCases {
		if err := checkReplyOutputMedia(tc.evs); err == nil {
			t.Errorf("%s: checkReplyOutputMedia returned nil, want error", tc.name)
		}
	}
}
