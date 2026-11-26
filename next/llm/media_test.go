package llm

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestOutputMediaJSONAndValidation(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF\x24\x00\x00\x00WAVEfmt "))
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))

	ev := Event{
		Type:  EventMedia,
		Media: &OutputMedia{Kind: MediaImage, MediaType: "image/png", Data: png},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != EventMedia || got.Media == nil || got.Media.Kind != MediaImage || got.Media.MediaType != "image/png" || got.Media.Data != png {
		t.Fatalf("round trip got %+v, want %+v", got, ev)
	}

	var empty Event
	if err := json.Unmarshal([]byte(`{"type":"finish","finish":{"reason":"stop"},"media":null}`), &empty); err != nil {
		t.Fatalf("unmarshal null media: %v", err)
	}
	if empty.Media != nil {
		t.Fatalf("null media unmarshaled to %+v, want nil", empty.Media)
	}

	if err := ValidOutputMedia(&OutputMedia{Kind: MediaImage, MediaType: "image/png", Data: png}); err != nil {
		t.Errorf("valid png: %v", err)
	}
	if err := ValidOutputMedia(&OutputMedia{Kind: MediaAudio, MediaType: "audio/L16;codec=pcm;rate=24000", Data: wav}); err != nil {
		t.Errorf("valid pcm audio: %v", err)
	}
	bad := []*OutputMedia{
		nil,
		{Kind: MediaImage, MediaType: "image/svg+xml", Data: svg},
		{Kind: MediaImage, MediaType: "IMAGE/SVG+XML;charset=utf-8", Data: svg},
		{Kind: MediaImage, MediaType: "application/pdf", Data: png},
		{Kind: MediaImage, MediaType: "image/png", Data: "not base64!!!"},
		{Kind: MediaImage, MediaType: "image/png", Data: ""},
		{Kind: MediaAudio, MediaType: "image/png", Data: png},
		{Kind: MediaImage, MediaType: "audio/wav", Data: wav},
		{Kind: "video", MediaType: "video/mp4", Data: png},
	}
	for i, m := range bad {
		if err := ValidOutputMedia(m); err == nil {
			t.Errorf("bad[%d] (%+v): got nil error, want error", i, m)
		}
	}

	if mt := SniffImageMediaType(png); mt != "image/png" {
		t.Errorf("sniff png = %q, want image/png", mt)
	}
	if mt := SniffImageMediaType(svg); mt != "image/svg+xml" {
		t.Errorf("sniff svg = %q, want image/svg+xml", mt)
	}
	if mt := SniffAudioMediaType(wav, "audio/pcm"); mt != "audio/wav" {
		t.Errorf("sniff wav = %q, want audio/wav", mt)
	}
	if mt := SniffAudioMediaType(base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3, 4, 5, 6, 7}), "audio/pcm"); mt != "audio/pcm" {
		t.Errorf("sniff pcm fallback = %q, want audio/pcm", mt)
	}
}

func TestSequenceMediaEvent(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	var seq Sequence
	evs := []Event{
		{Type: EventResponseStart, Response: &ResponseInfo{ID: "r1", Model: "m"}},
		{Type: EventMedia, Media: &OutputMedia{Kind: MediaImage, MediaType: "image/png", Data: png}},
		{Type: EventFinish, Finish: &Finish{Reason: FinishStop}},
	}
	for _, ev := range evs {
		if err := seq.Check(ev); err != nil {
			t.Fatalf("Check(%s): %v", ev.Type, err)
		}
	}

	var bad Sequence
	_ = bad.Check(Event{Type: EventResponseStart, Response: &ResponseInfo{ID: "r1", Model: "m"}})
	if err := bad.Check(Event{Type: EventMedia}); err == nil || !strings.Contains(err.Error(), "media") {
		t.Fatalf("Check(EventMedia without Media) = %v, want error", err)
	}
}

func TestSafeOutputMediaKind(t *testing.T) {
	good := map[string]string{
		"image/png":                      MediaImage,
		" Image/JPEG ":                   MediaImage,
		"image/webp; q=1":                MediaImage,
		"audio/wav":                      MediaAudio,
		"audio/L16;codec=pcm;rate=24000": MediaAudio,
		"audio/pcm;rate=24000":           MediaAudio,
		"AUDIO/MPEG":                     MediaAudio,
		"audio/ogg; codecs=\"opus\"":     MediaAudio,
	}
	for mt, want := range good {
		if kind, problem := SafeOutputMediaKind(mt); kind != want || problem != "" {
			t.Errorf("SafeOutputMediaKind(%q) = %q, %q; want %q", mt, kind, problem, want)
		}
	}
	for _, mt := range []string{
		"", "   ", "image", "image/", "/png", "image/svg+xml", "IMAGE/SVG+XML;charset=utf-8", " image/svg+xml ",
		"image/svg+xml,<svg/onload=alert(1)>", "image/svg+xml,", "image/svg+xml <svg>", "image/svg+xml\n",
		"image/png,<svg>", "image/png; charset", "image/png\x00", "image/ png", "image/png/extra",
		"image/svg", "image/x-unlisted", "audio/x-unlisted", "text/html", "application/pdf", "video/mp4",
	} {
		if kind, problem := SafeOutputMediaKind(mt); kind != "" || problem == "" {
			t.Errorf("SafeOutputMediaKind(%q) = %q, %q; want a refusal", mt, kind, problem)
		}
	}
}
