package e2e

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// TestOpenAICompatibleAudioRoutes drives speech, transcription and translation through core and
// the OpenAI-compatible extension to a fake upstream. Speech must keep its binary bytes and
// Content-Type; the multipart routes must keep every form field, the uploaded file's metadata and
// bytes, and the upstream reply.
func TestOpenAICompatibleAudioRoutes(t *testing.T) {
	const (
		speechReply = "ID3\x00\xff\x80audio\x00bytes"
		transcript  = `{"text":"hello from speech"}`
		translation = `{"text":"hello in English"}`
	)
	wav := []byte("RIFF\x00\xffWAVEfmt data\x00\x01")
	type upload struct {
		fields      map[string][]string
		filename    string
		contentType string
		data        []byte
	}
	var (
		mu       sync.Mutex
		speech   []byte
		uploads  = map[string]upload{}
		requests int
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini-tts"},{"id":"whisper-1"}]}`)
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/audio/speech":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			speech = b
			mu.Unlock()
			w.Header().Set("Content-Type", "audio/mpeg")
			io.WriteString(w, speechReply)
		case "/v1/audio/transcriptions", "/v1/audio/translations":
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f, fh, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer f.Close()
			data, _ := io.ReadAll(f)
			u := upload{fields: map[string][]string{}, filename: fh.Filename, contentType: fh.Header.Get("Content-Type"), data: data}
			for key, values := range r.MultipartForm.Value {
				u.fields[key] = append([]string(nil), values...)
			}
			mu.Lock()
			uploads[r.URL.Path] = u
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/audio/transcriptions" {
				io.WriteString(w, transcript)
			} else {
				io.WriteString(w, translation)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)

	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "audio-acct",
				"settings":   map[string]any{"base_url": up.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	})

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
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="file"; filename="sample.wav"`)
		header.Set("Content-Type", "audio/wav")
		part, err := mw.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(wav); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		return mw.FormDataContentType(), body.Bytes()
	}

	t.Run("speech", func(t *testing.T) {
		body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy","response_format":"mp3","speed":1.25}`
		code, header, reply := post("/v1/audio/speech", "application/json", []byte(body))
		if code != http.StatusOK || !bytes.Equal(reply, []byte(speechReply)) {
			t.Fatalf("speech: got %d %q, want 200 %q", code, reply, speechReply)
		}
		if got := header.Get("Content-Type"); got != "audio/mpeg" {
			t.Fatalf("speech Content-Type = %q, want audio/mpeg", got)
		}
		mu.Lock()
		got := append([]byte(nil), speech...)
		mu.Unlock()
		if !bytes.Equal(got, []byte(body)) {
			t.Errorf("upstream speech body = %s, want unchanged %s", got, body)
		}
	})

	t.Run("transcriptions", func(t *testing.T) {
		contentType, body := multipartBody([][2]string{
			{"model", "whisper-1"},
			{"response_format", "verbose_json"},
			{"language", "en"},
			{"temperature", "0.2"},
			{"timestamp_granularities[]", "word"},
			{"timestamp_granularities[]", "segment"},
		})
		code, header, reply := post("/v1/audio/transcriptions", contentType, body)
		if code != http.StatusOK || string(reply) != transcript || header.Get("Content-Type") != "application/json" {
			t.Fatalf("transcription: got %d %q %q", code, header.Get("Content-Type"), reply)
		}
		mu.Lock()
		got := uploads["/v1/audio/transcriptions"]
		mu.Unlock()
		wantFields := map[string][]string{
			"model":                     {"whisper-1"},
			"response_format":           {"verbose_json"},
			"language":                  {"en"},
			"temperature":               {"0.2"},
			"timestamp_granularities[]": {"word", "segment"},
		}
		if !reflect.DeepEqual(got.fields, wantFields) || got.filename != "sample.wav" || got.contentType != "audio/wav" || !bytes.Equal(got.data, wav) {
			t.Errorf("transcription upload = %+v, want fields %v, sample.wav audio/wav %q", got, wantFields, wav)
		}
	})

	t.Run("translations", func(t *testing.T) {
		contentType, body := multipartBody([][2]string{
			{"model", "whisper-1"},
			{"prompt", "names"},
			{"response_format", "json"},
			{"temperature", "0"},
		})
		code, header, reply := post("/v1/audio/translations", contentType, body)
		if code != http.StatusOK || string(reply) != translation || header.Get("Content-Type") != "application/json" {
			t.Fatalf("translation: got %d %q %q", code, header.Get("Content-Type"), reply)
		}
		mu.Lock()
		got := uploads["/v1/audio/translations"]
		mu.Unlock()
		wantFields := map[string][]string{
			"model":           {"whisper-1"},
			"prompt":          {"names"},
			"response_format": {"json"},
			"temperature":     {"0"},
		}
		if !reflect.DeepEqual(got.fields, wantFields) || got.filename != "sample.wav" || got.contentType != "audio/wav" || !bytes.Equal(got.data, wav) {
			t.Errorf("translation upload = %+v, want fields %v, sample.wav audio/wav %q", got, wantFields, wav)
		}
	})

	mu.Lock()
	gotRequests := requests
	mu.Unlock()
	if gotRequests != 3 {
		t.Errorf("upstream got %d audio requests, want 3", gotRequests)
	}
}
