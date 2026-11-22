package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
)

// TestGeminiAudioRoutesE2E exercises POST /v1/audio/speech and POST /v1/audio/transcriptions
// through a real core against a fake Gemini upstream, and verifies that unsupported formats
// (and /v1/audio/translations) fall through to a second account on the same model.
func TestGeminiAudioRoutesE2E(t *testing.T) {
	pcm := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60}
	pcmB64 := base64.StdEncoding.EncodeToString(pcm)
	sampleWAV := []byte("RIFF\x24\x00\x00\x00WAVEfmt data\x00\x01")

	gemUp := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1beta/models/gemini-audio:generateContent":
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), `"AUDIO"`) {
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
						"finishReason": "STOP",
					}},
					"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 9},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": "transcribed by gemini"}},
					},
					"finishReason": "STOP",
				}},
				"usageMetadata": map[string]any{"promptTokenCount": 11, "candidatesTokenCount": 4},
			})
		default:
			http.NotFound(w, r)
		}
	})

	var (
		oaMu    sync.Mutex
		oaPaths []string
	)
	oaUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			oaMu.Lock()
			oaPaths = append(oaPaths, r.URL.Path)
			oaMu.Unlock()
		}
		switch r.URL.Path {
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "ID3-fallback-mp3")
		case "/v1/audio/transcriptions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"text":"verbose fallback","duration":1.5}`)
		case "/v1/audio/translations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"text":"translated fallback"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(oaUp.Close)

	base, _, logs := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{map[string]any{
				"id":         "gemini-first",
				"models":     []string{"gemini-audio"},
				"settings":   map[string]any{"base_url": gemUp.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-gemini-key"},
			}},
		},
		"extensions": map[string]any{
			"pluxy/openai-compatible": map[string]any{
				"accounts": []any{map[string]any{
					"id":         "openai-second",
					"models":     []string{"openai-audio"},
					"settings":   map[string]any{"base_url": oaUp.URL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
				}},
			},
		},
		"routing": map[string]any{
			"chains": map[string]any{
				"shared-audio": []any{
					map[string]any{"model": "gemini-audio", "provider": "pluxy/gemini"},
					map[string]any{"model": "openai-audio", "provider": "pluxy/openai-compatible"},
				},
			},
		},
	}, "-bundled", bundledRegistry(t, map[string]string{
		"pluxy/openai-compatible": "openai-compatible",
	}))
	logs.waitFor(t, "config revision 1 active")
	installBundled(t, base, "pluxy/openai-compatible")

	post := func(path, contentType string, body []byte) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+clientKey)
		req.Header.Set("Content-Type", contentType)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		reply, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Clone(), reply
	}

	multipartBody := func(fields [][2]string) (string, []byte) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for _, field := range fields {
			if err := mw.WriteField(field[0], field[1]); err != nil {
				t.Fatal(err)
			}
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="clip.wav"`)
		h.Set("Content-Type", "audio/wav")
		part, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(sampleWAV)
		_ = mw.Close()
		return mw.FormDataContentType(), body.Bytes()
	}

	t.Run("speech pcm served by gemini", func(t *testing.T) {
		code, h, reply := post("/v1/audio/speech", "application/json", []byte(`{"model":"shared-audio","input":"hello","voice":"alloy","response_format":"pcm"}`))
		if code != http.StatusOK || h.Get("Content-Type") != "audio/pcm" || !bytes.Equal(reply, pcm) {
			t.Fatalf("got %d %q %x, want 200 audio/pcm %x", code, h.Get("Content-Type"), reply, pcm)
		}
	})

	t.Run("speech wav served by gemini", func(t *testing.T) {
		code, h, reply := post("/v1/audio/speech", "application/json", []byte(`{"model":"shared-audio","input":"hello","voice":"Kore","response_format":"wav"}`))
		if code != http.StatusOK || h.Get("Content-Type") != "audio/wav" {
			t.Fatalf("got %d %q %q", code, h.Get("Content-Type"), reply)
		}
		if len(reply) != 44+len(pcm) || string(reply[:4]) != "RIFF" || binary.LittleEndian.Uint32(reply[24:28]) != 24000 || !bytes.Equal(reply[44:], pcm) {
			t.Fatalf("unexpected wav reply: len=%d header=%q", len(reply), reply[:min(len(reply), 44)])
		}
	})

	t.Run("speech mp3 falls through to second account", func(t *testing.T) {
		code, h, reply := post("/v1/audio/speech", "application/json", []byte(`{"model":"shared-audio","input":"hello","voice":"alloy","response_format":"mp3"}`))
		if code != http.StatusOK || h.Get("Content-Type") != "audio/mpeg" || string(reply) != "ID3-fallback-mp3" {
			t.Fatalf("got %d %q %q", code, h.Get("Content-Type"), reply)
		}
	})

	t.Run("transcription json served by gemini", func(t *testing.T) {
		ct, body := multipartBody([][2]string{{"model", "shared-audio"}, {"response_format", "json"}})
		code, h, reply := post("/v1/audio/transcriptions", ct, body)
		if code != http.StatusOK || h.Get("Content-Type") != "application/json" || string(reply) != `{"text":"transcribed by gemini"}` {
			t.Fatalf("got %d %q %s", code, h.Get("Content-Type"), reply)
		}
	})

	t.Run("transcription text served by gemini", func(t *testing.T) {
		ct, body := multipartBody([][2]string{{"model", "shared-audio"}, {"response_format", "text"}})
		code, h, reply := post("/v1/audio/transcriptions", ct, body)
		if code != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") || string(reply) != "transcribed by gemini" {
			t.Fatalf("got %d %q %q", code, h.Get("Content-Type"), reply)
		}
	})

	t.Run("transcription verbose_json falls through to second account", func(t *testing.T) {
		ct, body := multipartBody([][2]string{{"model", "shared-audio"}, {"response_format", "verbose_json"}})
		code, _, reply := post("/v1/audio/transcriptions", ct, body)
		if code != http.StatusOK || string(reply) != `{"text":"verbose fallback","duration":1.5}` {
			t.Fatalf("got %d %s", code, reply)
		}
	})

	t.Run("translations falls through to second account", func(t *testing.T) {
		ct, body := multipartBody([][2]string{{"model", "shared-audio"}})
		code, _, reply := post("/v1/audio/translations", ct, body)
		if code != http.StatusOK || string(reply) != `{"text":"translated fallback"}` {
			t.Fatalf("got %d %s", code, reply)
		}
	})

	oaMu.Lock()
	gotOA := append([]string(nil), oaPaths...)
	oaMu.Unlock()
	if len(gotOA) != 3 {
		t.Fatalf("second account saw %v, want 3 fallback requests", gotOA)
	}
}
