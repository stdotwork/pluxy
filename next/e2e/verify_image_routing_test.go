package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

type verifyImageRequest struct {
	path          string
	contentType   string
	authorization string
	body          []byte
}

type verifyImageUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []verifyImageRequest
}

func newVerifyImageUpstream(t *testing.T, handle func(http.ResponseWriter, *http.Request, []byte)) *verifyImageUpstream {
	t.Helper()
	u := &verifyImageUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		u.mu.Lock()
		u.seen = append(u.seen, verifyImageRequest{
			path:          r.URL.Path,
			contentType:   r.Header.Get("Content-Type"),
			authorization: r.Header.Get("Authorization"),
			body:          append([]byte(nil), body...),
		})
		u.mu.Unlock()
		handle(w, r, body)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *verifyImageUpstream) requests() []verifyImageRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]verifyImageRequest(nil), u.seen...)
}

func startVerifyCodexImagesCore(t *testing.T, upstreamURL string) string {
	t.Helper()
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	return runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "codex-images",
				"settings":   map[string]any{"base_url": strings.TrimRight(upstreamURL, "/") + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-e2e-images"},
			}},
		},
	})
}

func verifyImagePost(t *testing.T, base, path, contentType string, body []byte) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
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
	reply, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, reply
}

func verifyBytesSummary(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%d bytes, sha256 %x", len(b), sum)
}

func verifyAddImagePart(t *testing.T, w *multipart.Writer, field, filename string, data []byte) {
	t.Helper()
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filename))
	h.Set("Content-Type", "image/png")
	p, err := w.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(data); err != nil {
		t.Fatal(err)
	}
}

// A long JSON generations request keeps every field, and a multi-megabyte image reply keeps its
// data intact instead of being cut by a fixed request or reply limit.
func TestVerifyImagesLongGenerationRoundTrip(t *testing.T) {
	prompt := strings.Repeat("a detailed night city, rain, reflections, ", 50000)
	image := bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 512*1024)
	reply, err := json.Marshal(map[string]any{
		"created":       1700000000,
		"background":    "transparent",
		"output_format": "png",
		"quality":       "high",
		"size":          "1536x1024",
		"data":          []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(image)}},
		"usage":         map[string]any{"input_tokens": 12, "output_tokens": 2048, "total_tokens": 2060},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := newVerifyImageUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	})
	base := startVerifyCodexImagesCore(t, u.URL)
	body, err := json.Marshal(map[string]any{
		"model":             "gpt-image-2",
		"prompt":            prompt,
		"size":              "1536x1024",
		"quality":           "high",
		"background":        "transparent",
		"output_format":     "png",
		"n":                 1,
		"partial_images":    1,
		"transparent_png":   true,
		"client_request_id": "long-generation-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, got := verifyImagePost(t, base, "/v1/images/generations", "application/json", body)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, got)
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("reply changed: got %s, want %s", verifyBytesSummary(got), verifyBytesSummary(reply))
	}
	reqs := u.requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.path != "/v1/images/generations" || req.contentType != "application/json" || req.authorization != "Bearer sk-e2e-images" {
		t.Fatalf("upstream request = %s, content type %q, authorization %q", req.path, req.contentType, req.authorization)
	}
	var sent struct {
		Model           string `json:"model"`
		Prompt          string `json:"prompt"`
		Size            string `json:"size"`
		Quality         string `json:"quality"`
		Background      string `json:"background"`
		OutputFormat    string `json:"output_format"`
		N               int    `json:"n"`
		PartialImages   int    `json:"partial_images"`
		TransparentPNG  bool   `json:"transparent_png"`
		ClientRequestID string `json:"client_request_id"`
	}
	if err := json.Unmarshal(req.body, &sent); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if sent.Prompt != prompt {
		t.Fatalf("prompt changed: got %s, want %s", verifyBytesSummary([]byte(sent.Prompt)), verifyBytesSummary([]byte(prompt)))
	}
	if sent.Model != "gpt-image-2" || sent.Size != "1536x1024" || sent.Quality != "high" || sent.Background != "transparent" ||
		sent.OutputFormat != "png" || sent.N != 1 || sent.PartialImages != 1 || !sent.TransparentPNG || sent.ClientRequestID != "long-generation-1" {
		t.Fatalf("upstream fields changed: %+v", sent)
	}
	if len(req.body) < 1<<20 {
		t.Fatalf("upstream body is only %d bytes; the long request was not preserved", len(req.body))
	}
}

// A long multipart edit keeps its image bytes, mask, prompt and options when core converts the form
// to the JSON body the Codex Images API takes, and the image reply comes back whole.
func TestVerifyImagesMultipartEditRoundTrip(t *testing.T) {
	prompt := strings.TrimSpace(strings.Repeat("make the product label readable, keep the shadows, ", 25000))
	image := bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 256*1024)
	mask := bytes.Repeat([]byte{0x00, 0xff, 0x00, 0xff}, 128*1024)
	imageReply := bytes.Repeat([]byte{'i', 'm', 'a', 'g', 'e'}, 300000)
	reply, err := json.Marshal(map[string]any{
		"created": 1700000001,
		"data":    []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(imageReply)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := newVerifyImageUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	})
	base := startVerifyCodexImagesCore(t, u.URL)
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	for _, field := range [][2]string{
		{"model", "gpt-image-2"},
		{"prompt", prompt},
		{"n", "1"},
		{"background", "transparent"},
		{"output_format", "png"},
		{"input_fidelity", "high"},
	} {
		if err := mw.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	verifyAddImagePart(t, mw, "image[]", "source.png", image)
	verifyAddImagePart(t, mw, "mask", "mask.png", mask)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	status, got := verifyImagePost(t, base, "/v1/images/edits", mw.FormDataContentType(), form.Bytes())
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, got)
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("reply changed: got %s, want %s", verifyBytesSummary(got), verifyBytesSummary(reply))
	}
	reqs := u.requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.path != "/v1/images/edits" || req.contentType != "application/json" || req.authorization != "Bearer sk-e2e-images" {
		t.Fatalf("upstream request = %s, content type %q, authorization %q", req.path, req.contentType, req.authorization)
	}
	var sent struct {
		Model         string `json:"model"`
		Prompt        string `json:"prompt"`
		N             int    `json:"n"`
		Background    string `json:"background"`
		OutputFormat  string `json:"output_format"`
		InputFidelity string `json:"input_fidelity"`
		Images        []struct {
			ImageURL string `json:"image_url"`
		} `json:"images"`
		Mask struct {
			ImageURL string `json:"image_url"`
		} `json:"mask"`
	}
	if err := json.Unmarshal(req.body, &sent); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if sent.Prompt != prompt {
		t.Fatalf("prompt changed: got %s, want %s", verifyBytesSummary([]byte(sent.Prompt)), verifyBytesSummary([]byte(prompt)))
	}
	if sent.Model != "gpt-image-2" || sent.N != 1 || sent.Background != "transparent" || sent.OutputFormat != "png" || sent.InputFidelity != "high" {
		t.Fatalf("upstream fields changed: %+v", sent)
	}
	if len(sent.Images) != 1 || sent.Images[0].ImageURL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(image) {
		t.Fatalf("edit image was not preserved: %d images", len(sent.Images))
	}
	if sent.Mask.ImageURL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(mask) {
		t.Fatalf("edit mask was not preserved: got %s, want %s",
			verifyBytesSummary([]byte(sent.Mask.ImageURL)), verifyBytesSummary([]byte("data:image/png;base64,"+base64.StdEncoding.EncodeToString(mask))))
	}
}
