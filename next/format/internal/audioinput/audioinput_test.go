package audioinput

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// MIME and Format map the audio format tokens the OpenAI APIs use to media types and back, as
// today's core mapped them for Gemini.
func TestFormatAndMIME(t *testing.T) {
	for format, mime := range map[string]string{
		"":           "audio/wav",
		"wav":        "audio/wav",
		"mp3":        "audio/mpeg",
		"ogg":        "audio/ogg",
		"flac":       "audio/flac",
		"aac":        "audio/aac",
		"webm":       "audio/webm",
		"pcm16":      "audio/pcm",
		"g711_ulaw":  "audio/basic",
		"g711_alaw":  "audio/basic",
		"aiff":       "audio/aiff",
		"audio/opus": "audio/audio/opus",
	} {
		if got := MIME(format); got != mime {
			t.Errorf("MIME(%q) = %q, want %q", format, got, mime)
		}
	}
	for mime, format := range map[string]string{
		"audio/wav":   "wav",
		"audio/mpeg":  "mp3",
		"audio/pcm":   "pcm16",
		"audio/basic": "g711_ulaw",
		"audio/aiff":  "aiff",
	} {
		if got := Format(mime); got != format {
			t.Errorf("Format(%q) = %q, want %q", mime, got, format)
		}
	}
	if got := MIME(Format("audio/ogg")); got != "audio/ogg" {
		t.Errorf("MIME(Format(audio/ogg)) = %q, want it back", got)
	}
	for mime, format := range map[string]string{
		"AUDIO/WAV":               "wav",
		"Audio/MPEG":              "mp3",
		"AUDIO/OGG":               "ogg",
		"AUDIO/WAV;charset=utf-8": "wav",
	} {
		if got := Format(mime); got != format {
			t.Errorf("Format(%q) = %q, want %q", mime, got, format)
		}
	}
	mt, data, ok := Inline(llm.Part{Type: llm.PartFile, FileData: "DATA:AUDIO/WAV;BASE64,SUQzBA=="})
	if !ok || mt != "audio/wav" || data != "SUQzBA==" {
		t.Errorf("Inline(mixed-case) = (%q, %q, %v), want (audio/wav, SUQzBA==, true)", mt, data, ok)
	}
}
