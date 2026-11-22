package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestGeminiAudioSpeechPCMAndWAV(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	pcmB64 := base64.StdEncoding.EncodeToString(pcm)

	var seenPaths []string
	var seenBodies []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		seenPaths = append(seenPaths, r.URL.Path)
		seenBodies = append(seenBodies, body)
		w.Header().Set("Content-Type", "application/json")
		rate := "24000"
		if strings.Contains(r.URL.Path, "16k") {
			rate = "16000"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{
					"role": "model",
					"parts": []any{map[string]any{
						"inlineData": map[string]any{
							"mimeType": "audio/L16;codec=pcm;rate=" + rate,
							"data":     pcmB64,
						},
					}},
				},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount":     5,
				"candidatesTokenCount": 12,
			},
		})
	}))
	defer up.Close()

	call := func(model, body string) (*recorder, error) {
		rec := &recorder{}
		err := audioSpeechTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-audio",
				Settings:   json.RawMessage(`{"base_url":` + mustJSON(up.URL) + `}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: llm.Request{
				Model:  model,
				Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(body)},
			},
		}, rec)
		return rec, err
	}

	t.Run("pcm maps OpenAI voice and returns raw PCM", func(t *testing.T) {
		rec, err := call("gemini-2.5-flash-preview-tts", `{"model":"gemini-2.5-flash-preview-tts","input":"Hello world","voice":"alloy","response_format":"pcm","speed":1}`)
		if err != nil {
			t.Fatalf("audioSpeechTo: %v", err)
		}
		ct, data, usage := collectAudioReply(t, rec.events, true)
		if ct != "audio/pcm" {
			t.Fatalf("Content-Type = %q, want audio/pcm", ct)
		}
		if !bytes.Equal(data, pcm) {
			t.Fatalf("PCM bytes = %x, want %x", data, pcm)
		}
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 5 || usage.OutputTokens == nil || *usage.OutputTokens != 12 {
			t.Fatalf("usage = %+v, want input=5 output=12", usage)
		}
		got := seenBodies[len(seenBodies)-1]
		genCfg, _ := got["generationConfig"].(map[string]any)
		speechCfg, _ := genCfg["speechConfig"].(map[string]any)
		voiceCfg, _ := speechCfg["voiceConfig"].(map[string]any)
		prebuilt, _ := voiceCfg["prebuiltVoiceConfig"].(map[string]any)
		if prebuilt["voiceName"] != "Kore" {
			t.Fatalf("voiceName = %v, want Kore (alloy mapped)", prebuilt["voiceName"])
		}
	})

	t.Run("wav wraps PCM with RIFF header using upstream rate and keeps Gemini voice", func(t *testing.T) {
		rec, err := call("gemini-tts-16k", `{"model":"gemini-tts-16k","input":"Hi","voice":"Puck","response_format":"wav"}`)
		if err != nil {
			t.Fatalf("audioSpeechTo: %v", err)
		}
		ct, data, _ := collectAudioReply(t, rec.events, true)
		if ct != "audio/wav" {
			t.Fatalf("Content-Type = %q, want audio/wav", ct)
		}
		if len(data) != 44+len(pcm) {
			t.Fatalf("WAV len = %d, want %d", len(data), 44+len(pcm))
		}
		if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[12:16]) != "fmt " || string(data[36:40]) != "data" {
			t.Fatalf("bad WAV header: %q", data[:44])
		}
		if got := binary.LittleEndian.Uint32(data[24:28]); got != 16000 {
			t.Errorf("sampleRate = %d, want 16000", got)
		}
		if got := binary.LittleEndian.Uint16(data[22:24]); got != 1 {
			t.Errorf("channels = %d, want 1", got)
		}
		if got := binary.LittleEndian.Uint16(data[34:36]); got != 16 {
			t.Errorf("bitsPerSample = %d, want 16", got)
		}
		if got := binary.LittleEndian.Uint32(data[40:44]); got != uint32(len(pcm)) {
			t.Errorf("dataSize = %d, want %d", got, len(pcm))
		}
		if !bytes.Equal(data[44:], pcm) {
			t.Errorf("WAV payload = %x, want %x", data[44:], pcm)
		}
		got := seenBodies[len(seenBodies)-1]
		genCfg, _ := got["generationConfig"].(map[string]any)
		speechCfg, _ := genCfg["speechConfig"].(map[string]any)
		voiceCfg, _ := speechCfg["voiceConfig"].(map[string]any)
		prebuilt, _ := voiceCfg["prebuiltVoiceConfig"].(map[string]any)
		if prebuilt["voiceName"] != "Puck" {
			t.Fatalf("voiceName = %v, want Puck", prebuilt["voiceName"])
		}
	})

	t.Run("binary reply splits into standalone base64 pieces", func(t *testing.T) {
		prev := audioBinaryChunkSize
		audioBinaryChunkSize = 3
		defer func() { audioBinaryChunkSize = prev }()

		rec, err := call("gemini-2.5-flash-preview-tts", `{"model":"gemini-2.5-flash-preview-tts","input":"Chunk me","voice":"Kore","response_format":"pcm"}`)
		if err != nil {
			t.Fatalf("audioSpeechTo: %v", err)
		}
		var pieces int
		for _, ev := range rec.events {
			if ev.Type == llm.EventNative {
				pieces++
			}
		}
		if pieces < 3 {
			t.Fatalf("got %d native pieces, want >= 3", pieces)
		}
		_, data, _ := collectAudioReply(t, rec.events, true)
		if !bytes.Equal(data, pcm) {
			t.Fatalf("reassembled PCM = %x, want %x", data, pcm)
		}
	})
}

func TestGeminiAudioSpeechErrors(t *testing.T) {
	run := func(req llm.Request) *llm.Error {
		t.Helper()
		rec := &recorder{}
		err := audioSpeechTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-audio",
				Settings:   json.RawMessage(`{"base_url":"http://127.0.0.1:1"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: req,
		}, rec)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		e, ok := err.(*llm.Error)
		if !ok {
			t.Fatalf("error type %T (%v), want *llm.Error", err, err)
		}
		return e
	}

	unsupported := []struct {
		name  string
		req   llm.Request
		field string
	}{
		{"default mp3 format", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy"}`)}}, "response_format"},
		{"explicit mp3", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"mp3"}`)}}, "response_format"},
		{"opus", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"opus"}`)}}, "response_format"},
		{"aac", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"aac"}`)}}, "response_format"},
		{"flac", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"flac"}`)}}, "response_format"},
		{"unknown response_format ulaw", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"ulaw"}`)}}, "response_format"},
		{"sse stream_format", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"pcm","stream_format":"sse"}`)}}, "stream_format"},
		{"unknown stream_format", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"pcm","stream_format":"chunked"}`)}}, "stream_format"},
		{"stream on speech", llm.Request{Model: "tts", Stream: true, Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"pcm","stream":true}`)}}, "stream"},
		{"speed != 1", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"pcm","speed":1.25}`)}}, "speed"},
		{"speed out of range", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"pcm","speed":5.0}`)}}, "speed"},
		{"instructions", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"pcm","instructions":"whisper"}`)}}, "instructions"},
		{"custom voice object", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":{"id":"voice_123"},"response_format":"pcm"}`)}}, "voice"},
		{"nova voice", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"nova","response_format":"pcm"}`)}}, "voice"},
		{"thinking suffix", llm.Request{Model: "tts", Options: llm.Options{ThinkingSuffix: "high"}, Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","voice":"alloy","response_format":"pcm"}`)}}, "model"},
	}
	for _, tc := range unsupported {
		t.Run("unsupported/"+tc.name, func(t *testing.T) {
			e := run(tc.req)
			if e.Code != llm.CodeUnsupported || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeCredential || !e.Retryable {
				t.Fatalf("got %+v, want CodeUnsupported/400/ScopeCredential/Retryable", e)
			}
			if !strings.Contains(e.Message, tc.field) {
				t.Errorf("message %q does not name %q", e.Message, tc.field)
			}
		})
	}

	invalid := []struct {
		name string
		req  llm.Request
	}{
		{"missing input", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","voice":"alloy","response_format":"pcm"}`)}}},
		{"empty input", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"","voice":"alloy","response_format":"pcm"}`)}}},
		{"missing voice", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"tts","input":"hi","response_format":"pcm"}`)}}},
		{"non-json body", llm.Request{Model: "tts", Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`not json`)}}},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			e := run(tc.req)
			if e.Code != llm.CodeInvalidRequest || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeRequest || e.Retryable {
				t.Fatalf("got %+v, want CodeInvalidRequest/400/ScopeRequest/non-retryable", e)
			}
		})
	}
}

func TestGeminiAudioTranscriptions(t *testing.T) {
	audioBytes := []byte("RIFF\x24\x00\x00\x00WAVEfmt ")
	audioB64 := base64.StdEncoding.EncodeToString(audioBytes)

	var lastBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &lastBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{
					"role": "model",
					"parts": []any{
						map[string]any{"text": "internal thinking", "thought": true},
						map[string]any{"text": "Hello from Gemini."},
					},
				},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount":     20,
				"candidatesTokenCount": 4,
			},
		})
	}))
	defer up.Close()

	call := func(body string) (*recorder, error) {
		rec := &recorder{}
		err := audioTranscriptionsTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-tx",
				Settings:   json.RawMessage(`{"base_url":` + mustJSON(up.URL) + `}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: llm.Request{
				Model:  "gemini-2.5-flash",
				Native: &llm.Native{Format: llm.FormatOpenAIAudioTranscriptions, Body: json.RawMessage(body)},
			},
		}, rec)
		return rec, err
	}

	t.Run("json format with language prompt temperature", func(t *testing.T) {
		body := `{"model":"gemini-2.5-flash","language":"en","prompt":"names","temperature":"0.2","file":{"filename":"clip.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`
		rec, err := call(body)
		if err != nil {
			t.Fatalf("audioTranscriptionsTo: %v", err)
		}
		ct, raw, usage := collectAudioReply(t, rec.events, false)
		if ct != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("json reply %q: %v", raw, err)
		}
		if out.Text != "Hello from Gemini." {
			t.Fatalf("text = %q, want %q (thought part must be excluded)", out.Text, "Hello from Gemini.")
		}
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 20 || usage.OutputTokens == nil || *usage.OutputTokens != 4 {
			t.Fatalf("usage = %+v", usage)
		}
		genCfg, _ := lastBody["generationConfig"].(map[string]any)
		if genCfg["temperature"] != 0.2 {
			t.Errorf("temperature = %v, want 0.2", genCfg["temperature"])
		}
	})

	t.Run("text format and mime inference from filename", func(t *testing.T) {
		body := `{"model":"gemini-2.5-flash","response_format":"text","file":{"filename":"clip.mp3","content_type":"application/octet-stream","data":` + mustJSON(audioB64) + `}}`
		rec, err := call(body)
		if err != nil {
			t.Fatalf("audioTranscriptionsTo: %v", err)
		}
		ct, raw, _ := collectAudioReply(t, rec.events, false)
		if !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("Content-Type = %q, want text/plain*", ct)
		}
		if string(raw) != "Hello from Gemini." {
			t.Fatalf("text = %q, want %q", raw, "Hello from Gemini.")
		}
		contents, _ := lastBody["contents"].([]any)
		parts, _ := contents[0].(map[string]any)["parts"].([]any)
		inline, _ := parts[1].(map[string]any)["inlineData"].(map[string]any)
		if inline["mimeType"] != "audio/mpeg" {
			t.Errorf("inlineData.mimeType = %v, want audio/mpeg", inline["mimeType"])
		}
	})
}

func TestGeminiAudioTranscriptionErrors(t *testing.T) {
	audioB64 := base64.StdEncoding.EncodeToString([]byte("RIFF\x24\x00\x00\x00WAVEfmt "))
	run := func(body string, stream bool) *llm.Error {
		t.Helper()
		rec := &recorder{}
		err := audioTranscriptionsTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-tx",
				Settings:   json.RawMessage(`{"base_url":"http://127.0.0.1:1"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: llm.Request{
				Model:  "gemini-2.5-flash",
				Stream: stream,
				Native: &llm.Native{Format: llm.FormatOpenAIAudioTranscriptions, Body: json.RawMessage(body)},
			},
		}, rec)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		e, ok := err.(*llm.Error)
		if !ok {
			t.Fatalf("error type %T (%v), want *llm.Error", err, err)
		}
		return e
	}

	unsupported := []struct {
		name   string
		body   string
		stream bool
		field  string
	}{
		{"verbose_json", `{"model":"m","response_format":"verbose_json","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "response_format"},
		{"srt", `{"model":"m","response_format":"srt","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "response_format"},
		{"vtt", `{"model":"m","response_format":"vtt","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "response_format"},
		{"diarized_json", `{"model":"m","response_format":"diarized_json","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "response_format"},
		{"timestamp_granularities", `{"model":"m","timestamp_granularities[]":["word"],"file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "timestamp_granularities"},
		{"stream", `{"model":"m","stream":true,"file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, true, "stream"},
		{"include logprobs", `{"model":"m","include[]":["logprobs"],"file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "include"},
		{"chunking_strategy", `{"model":"m","chunking_strategy":"auto","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "chunking_strategy"},
		{"unknown response_format", `{"model":"m","response_format":"xml","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "response_format"},
		{"temperature out of range", `{"model":"m","temperature":"2.5","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`, false, "temperature"},
		{"unrecognised file type", `{"model":"m","file":{"filename":"a.xyz","content_type":"application/octet-stream","data":` + mustJSON(audioB64) + `}}`, false, "file"},
	}
	for _, tc := range unsupported {
		t.Run("unsupported/"+tc.name, func(t *testing.T) {
			e := run(tc.body, tc.stream)
			if e.Code != llm.CodeUnsupported || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeCredential || !e.Retryable {
				t.Fatalf("got %+v, want CodeUnsupported/400/ScopeCredential/Retryable", e)
			}
			if !strings.Contains(e.Message, tc.field) {
				t.Errorf("message %q does not name %q", e.Message, tc.field)
			}
		})
	}

	t.Run("unsupported/thinking suffix", func(t *testing.T) {
		rec := &recorder{}
		err := audioTranscriptionsTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-tx",
				Settings:   json.RawMessage(`{"base_url":"http://127.0.0.1:1"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: llm.Request{
				Model:   "gemini-2.5-flash",
				Options: llm.Options{ThinkingSuffix: "high"},
				Native:  &llm.Native{Format: llm.FormatOpenAIAudioTranscriptions, Body: json.RawMessage(`{"model":"m","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`)},
			},
		}, rec)
		e, ok := err.(*llm.Error)
		if !ok || e.Code != llm.CodeUnsupported || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Fatalf("got %+v, want CodeUnsupported/400/ScopeCredential/Retryable", err)
		}
	})

	t.Run("unsupported/exceeds inline size limit", func(t *testing.T) {
		prev := maxGeminiInlineRequestBytes
		maxGeminiInlineRequestBytes = 100
		defer func() { maxGeminiInlineRequestBytes = prev }()

		big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 120))
		e := run(`{"model":"m","file":{"filename":"a.wav","content_type":"audio/wav","data":`+mustJSON(big)+`}}`, false)
		if e.Code != llm.CodeUnsupported || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Fatalf("got %+v, want CodeUnsupported/400/ScopeCredential/Retryable", e)
		}
		if !strings.Contains(e.Message, "file") {
			t.Errorf("message %q does not name file", e.Message)
		}
	})

	t.Run("invalid/bad base64", func(t *testing.T) {
		e := run(`{"model":"m","file":{"filename":"a.wav","content_type":"audio/wav","data":"!!!not-base64!!!"}}`, false)
		if e.Code != llm.CodeInvalidRequest || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeRequest || e.Retryable {
			t.Fatalf("got %+v, want CodeInvalidRequest/400/ScopeRequest/non-retryable", e)
		}
	})
}

func TestGeminiAudioSpeechUpstreamWAVAndVertex(t *testing.T) {
	pcm := []byte{0x11, 0x22, 0x33, 0x44}
	wav, err := wrapPCMAsWAV(pcm, 24000, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	wavB64 := base64.StdEncoding.EncodeToString(wav)

	var seenPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{
					"role": "model",
					"parts": []any{map[string]any{
						"inlineData": map[string]any{
							"mimeType": "audio/wav",
							"data":     wavB64,
						},
					}},
				},
				"finishReason": "STOP",
			}},
		})
	}))
	defer up.Close()

	vertexAcct := protocol.Account{
		ID:         "vertex-audio",
		Settings:   json.RawMessage(`{"base_url":` + mustJSON(up.URL) + `,"vertex":{"project":"p","location":"us-central1"}}`),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "vk"},
	}

	t.Run("wav passthrough on vertex", func(t *testing.T) {
		rec := &recorder{}
		err := audioSpeechTo(context.Background(), sdk.Call{
			Account: vertexAcct,
			Request: llm.Request{
				Model:  "gemini-2.5-flash-preview-tts",
				Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"gemini-2.5-flash-preview-tts","input":"hi","voice":"Kore","response_format":"wav"}`)},
			},
		}, rec)
		if err != nil {
			t.Fatalf("audioSpeechTo: %v", err)
		}
		ct, data, _ := collectAudioReply(t, rec.events, true)
		if ct != "audio/wav" || !bytes.Equal(data, wav) {
			t.Fatalf("got %q len=%d, want exact wav len=%d", ct, len(data), len(wav))
		}
		if !strings.Contains(seenPath, "/publishers/google/models/gemini-2.5-flash-preview-tts:generateContent") {
			t.Errorf("vertex path = %q", seenPath)
		}
	})

	t.Run("pcm strips upstream wav header", func(t *testing.T) {
		rec := &recorder{}
		err := audioSpeechTo(context.Background(), sdk.Call{
			Account: vertexAcct,
			Request: llm.Request{
				Model:  "gemini-2.5-flash-preview-tts",
				Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"gemini-2.5-flash-preview-tts","input":"hi","voice":"Kore","response_format":"pcm"}`)},
			},
		}, rec)
		if err != nil {
			t.Fatalf("audioSpeechTo: %v", err)
		}
		ct, data, _ := collectAudioReply(t, rec.events, true)
		if ct != "audio/pcm" || !bytes.Equal(data, pcm) {
			t.Fatalf("got %q %x, want audio/pcm %x", ct, data, pcm)
		}
	})
}

func TestGeminiAudioCutoffFinishReasonFails(t *testing.T) {
	pcmB64 := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03, 0x04})
	audioB64 := base64.StdEncoding.EncodeToString([]byte("RIFF\x24\x00\x00\x00WAVEfmt "))

	t.Run("speech partial audio with MAX_TOKENS", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{map[string]any{
							"inlineData": map[string]any{
								"mimeType": "audio/L16;codec=pcm;rate=24000",
								"data":     pcmB64,
							},
						}},
					},
					"finishReason": "MAX_TOKENS",
				}},
			})
		}))
		defer up.Close()

		rec := &recorder{}
		err := audioSpeechTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-audio",
				Settings:   json.RawMessage(`{"base_url":` + mustJSON(up.URL) + `}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: llm.Request{
				Model:  "gemini-2.5-flash-preview-tts",
				Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"gemini-2.5-flash-preview-tts","input":"hi","voice":"Kore","response_format":"pcm"}`)},
			},
		}, rec)
		e, ok := err.(*llm.Error)
		if !ok || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeRequest || !strings.Contains(e.Message, "MAX_TOKENS") {
			t.Fatalf("got %+v, want CodeUpstream/ScopeRequest naming MAX_TOKENS", err)
		}
	})

	t.Run("transcription partial text with MAX_TOKENS", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": "cut off transcript"}},
					},
					"finishReason": "MAX_TOKENS",
				}},
			})
		}))
		defer up.Close()

		rec := &recorder{}
		err := audioTranscriptionsTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-tx",
				Settings:   json.RawMessage(`{"base_url":` + mustJSON(up.URL) + `}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: llm.Request{
				Model:  "gemini-2.5-flash",
				Native: &llm.Native{Format: llm.FormatOpenAIAudioTranscriptions, Body: json.RawMessage(`{"model":"gemini-2.5-flash","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`)},
			},
		}, rec)
		e, ok := err.(*llm.Error)
		if !ok || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeRequest || !strings.Contains(e.Message, "MAX_TOKENS") {
			t.Fatalf("got %+v, want CodeUpstream/ScopeRequest naming MAX_TOKENS", err)
		}
	})

	t.Run("empty finishReason stays accepted", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": "complete without finishReason"}},
					},
				}},
			})
		}))
		defer up.Close()

		rec := &recorder{}
		err := audioTranscriptionsTo(context.Background(), sdk.Call{
			Account: protocol.Account{
				ID:         "gem-tx",
				Settings:   json.RawMessage(`{"base_url":` + mustJSON(up.URL) + `}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			},
			Request: llm.Request{
				Model:  "gemini-2.5-flash",
				Native: &llm.Native{Format: llm.FormatOpenAIAudioTranscriptions, Body: json.RawMessage(`{"model":"gemini-2.5-flash","file":{"filename":"a.wav","content_type":"audio/wav","data":` + mustJSON(audioB64) + `}}`)},
			},
		}, rec)
		if err != nil {
			t.Fatalf("expected empty finishReason to succeed, got %v", err)
		}
	})
}

func TestGeminiAudioBadUpstreamReplyIsScopeRequest(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}]}`)
	}))
	defer up.Close()

	rec := &recorder{}
	err := audioSpeechTo(context.Background(), sdk.Call{
		Account: protocol.Account{
			ID:         "gem-audio",
			Settings:   json.RawMessage(`{"base_url":` + mustJSON(up.URL) + `}`),
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
		},
		Request: llm.Request{
			Model:  "gemini-2.5-flash-preview-tts",
			Native: &llm.Native{Format: llm.FormatOpenAIAudioSpeech, Body: json.RawMessage(`{"model":"gemini-2.5-flash-preview-tts","input":"hi","voice":"alloy","response_format":"pcm"}`)},
		},
	}, rec)
	e, ok := err.(*llm.Error)
	if !ok || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeRequest {
		t.Fatalf("got %+v, want CodeUpstream on ScopeRequest", err)
	}
}

func collectAudioReply(t *testing.T, events []llm.Event, binaryBase64 bool) (string, []byte, *llm.Usage) {
	t.Helper()
	var (
		ct    string
		out   bytes.Buffer
		usage *llm.Usage
	)
	for _, ev := range events {
		switch ev.Type {
		case llm.EventResponseStart:
			var r llm.EndpointReply
			if err := json.Unmarshal(ev.Response.Raw, &r); err != nil {
				t.Fatalf("EndpointReply: %v", err)
			}
			ct = r.ContentType
		case llm.EventNative:
			var chunk string
			if err := json.Unmarshal(ev.Raw, &chunk); err != nil {
				t.Fatalf("EventNative: %v", err)
			}
			if binaryBase64 {
				b, err := base64.StdEncoding.DecodeString(chunk)
				if err != nil {
					t.Fatalf("chunk %q is not valid base64: %v", chunk, err)
				}
				out.Write(b)
			} else {
				out.WriteString(chunk)
			}
		case llm.EventFinish:
			usage = ev.Usage
		}
	}
	return ct, out.Bytes(), usage
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
