package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Images requests on an OpenAI-compatible account go to the account's own Images API:
// generations as JSON and edits as multipart form data. The upstream's reply comes back as it
// was written.
func TestOpenAICompatibleImages(t *testing.T) {
	const (
		apiKey        = "made-up-compat-images-key"
		imageModel    = "image-model"
		upstreamReply = `{"created":1700000000,"data":[{"b64_json":"aW1hZ2U="}],"usage":{"input_tokens":8,"output_tokens":16,"total_tokens":24}}`
	)
	type recordedRequest struct {
		method string
		path   string
		header http.Header
		body   []byte
	}
	var mu sync.Mutex
	var recorded []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"list","data":[{"id":"`+imageModel+`","object":"model"}]}`)
		case r.Method == http.MethodPost && (r.URL.Path == "/v1/images/generations" || r.URL.Path == "/v1/images/edits"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			recorded = append(recorded, recordedRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			io.WriteString(w, upstreamReply)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "compat-images",
				"models":     []string{imageModel},
				"settings":   map[string]any{"base_url": srv.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": apiKey},
			}},
		},
	})
	requests := func(path string) []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		var out []recordedRequest
		for _, r := range recorded {
			if r.path == path {
				out = append(out, r)
			}
		}
		return out
	}

	t.Run("generations", func(t *testing.T) {
		const body = `{"model":"image-model","prompt":"a cat","size":"1024x1024","x_vendor":{"keep":true}}`
		code, reply := clientPost(t, base, "/v1/images/generations", body, nil)
		if code != http.StatusOK || reply != upstreamReply {
			t.Fatalf("reply = %d %s, want 200 %s", code, reply, upstreamReply)
		}
		reqs := requests("/v1/images/generations")
		if len(reqs) != 1 {
			t.Fatalf("upstream got %d generations requests, want 1", len(reqs))
		}
		req := reqs[0]
		if req.method != http.MethodPost || req.header.Get("Authorization") != "Bearer "+apiKey {
			t.Errorf("upstream got %s with Authorization %q", req.method, req.header.Get("Authorization"))
		}
		if got := req.header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if string(req.body) != body {
			t.Errorf("body = %s, want unchanged %s", req.body, body)
		}
	})

	t.Run("edits", func(t *testing.T) {
		var form bytes.Buffer
		mw := multipart.NewWriter(&form)
		for _, field := range [][2]string{
			{"model", imageModel},
			{"prompt", "add a hat"},
			{"n", "2"},
			{"output_format", "png"},
		} {
			if err := mw.WriteField(field[0], field[1]); err != nil {
				t.Fatal(err)
			}
		}
		for _, file := range []struct{ name, data string }{
			{"a.png", "PNGDATA1"},
			{"b.png", "PNGDATA2"},
		} {
			part, err := mw.CreateFormFile("image[]", file.name)
			if err != nil {
				t.Fatal(err)
			}
			io.WriteString(part, file.data)
		}
		part, err := mw.CreateFormFile("mask", "mask.png")
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(part, "MASKDATA")
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/images/edits", bytes.NewReader(form.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+clientKey)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		replyBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(replyBody) != upstreamReply {
			t.Fatalf("reply = %d %s, want 200 %s", resp.StatusCode, replyBody, upstreamReply)
		}

		reqs := requests("/v1/images/edits")
		if len(reqs) != 1 {
			t.Fatalf("upstream got %d edits requests, want 1", len(reqs))
		}
		sent := reqs[0]
		if sent.method != http.MethodPost || sent.header.Get("Authorization") != "Bearer "+apiKey {
			t.Errorf("upstream got %s with Authorization %q", sent.method, sent.header.Get("Authorization"))
		}
		upstreamReq := &http.Request{Header: sent.header, Body: io.NopCloser(bytes.NewReader(sent.body))}
		if err := upstreamReq.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("upstream Content-Type %q, body %q: %v", sent.header.Get("Content-Type"), sent.body, err)
		}
		for field, want := range map[string]string{"model": imageModel, "prompt": "add a hat", "n": "2", "output_format": "png"} {
			if got := upstreamReq.MultipartForm.Value[field]; len(got) != 1 || got[0] != want {
				t.Errorf("form field %s = %v, want [%s]", field, got, want)
			}
		}
		images := upstreamReq.MultipartForm.File["image[]"]
		if len(images) != 2 {
			t.Fatalf("image[] files = %d, want 2", len(images))
		}
		for i, want := range []string{"PNGDATA1", "PNGDATA2"} {
			got, err := images[i].Open()
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(got)
			got.Close()
			if string(data) != want {
				t.Errorf("image %d = %q, want %q", i, data, want)
			}
		}
		masks := upstreamReq.MultipartForm.File["mask"]
		if len(masks) != 1 {
			t.Fatalf("mask files = %d, want 1", len(masks))
		}
		mask, err := masks[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		maskData, _ := io.ReadAll(mask)
		mask.Close()
		if string(maskData) != "MASKDATA" {
			t.Errorf("mask = %q, want MASKDATA", maskData)
		}

		var sentFields map[string]json.RawMessage
		if json.Unmarshal(sent.body, &sentFields) == nil {
			t.Errorf("upstream received JSON %s, want multipart/form-data", sent.body)
		}
	})
}
