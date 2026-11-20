package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A transcription's multipart body reaches the extension as a JSON form with its fields and
// uploaded file, and a binary speech reply is base64-decoded on the way to the client.
func TestAudioEndpoints(t *testing.T) {
	h, _, seen := endpointHandler(t, func(_ string, req llm.Request) []llm.Event {
		if req.Native.Format == llm.FormatOpenAIAudioSpeech {
			evs := bodyReply("audio/mpeg", base64.StdEncoding.EncodeToString([]byte("ID3\x00\xff")))
			evs[0].Response.Format = req.Native.Format
			return evs
		}
		evs := bodyReply("application/json", `{"text":"hello"}`)
		evs[0].Response.Format = req.Native.Format
		return evs
	})

	const file = "RIFF\x00\xffWAVE"
	b := "--x\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"response_format\"\r\n\r\nverbose_json\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"timestamp_granularities[]\"\r\n\r\nword\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"timestamp_granularities[]\"\r\n\r\nsegment\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"file\"; filename=\"sample.wav\"\r\nContent-Type: audio/wav\r\n\r\n" + file + "\r\n--x--\r\n"
	w := postEndpoint(h, "/v1/audio/transcriptions", b, map[string]string{"Content-Type": "multipart/form-data; boundary=x"})
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != `{"text":"hello"}` {
		t.Fatalf("transcription: got %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	req := (*seen)[0]
	if req.Model != "whisper-1" || req.Native.Format != llm.FormatOpenAIAudioTranscriptions {
		t.Fatalf("transcription request = %+v", req)
	}
	var form struct {
		Model       string   `json:"model"`
		Format      string   `json:"response_format"`
		Granularity []string `json:"timestamp_granularities[]"`
		File        struct {
			Filename    string `json:"filename"`
			ContentType string `json:"content_type"`
			Data        string `json:"data"`
		} `json:"file"`
	}
	if err := json.Unmarshal(req.Native.Body, &form); err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(form.File.Data)
	if form.Model != "whisper-1" || form.Format != "verbose_json" || strings.Join(form.Granularity, ",") != "word,segment" ||
		form.File.Filename != "sample.wav" || form.File.ContentType != "audio/wav" || !bytes.Equal(data, []byte(file)) {
		t.Fatalf("transcription form = %+v, file bytes %q", form, data)
	}

	w = postEndpoint(h, "/v1/audio/speech", `{"model":"tts-1","input":"hi","voice":"alloy"}`, nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" || w.Body.String() != "ID3\x00\xff" {
		t.Fatalf("speech: got %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if got := (*seen)[1].Native.Format; got != llm.FormatOpenAIAudioSpeech {
		t.Fatalf("speech format = %q", got)
	}
}

// A transcription without its file is refused before routing.
func TestAudioTranscriptionNeedsFile(t *testing.T) {
	h, accounts, _ := endpointHandler(t, func(string, llm.Request) []llm.Event { return nil })
	b := "--x\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n--x--\r\n"
	w := postEndpoint(h, "/v1/audio/transcriptions", b, map[string]string{"Content-Type": "multipart/form-data; boundary=x"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "file is required") || len(*accounts) != 0 {
		t.Fatalf("got %d %s after %v", w.Code, w.Body, *accounts)
	}
}
