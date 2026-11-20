package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A streaming image edit must reach the client as SSE even when Codex's native edits
// endpoint ignores stream=true and answers with one JSON Images API document. Otherwise an
// SSE client gets no image event, although the upstream generated and charged for the image.
func TestVerifyStreamingImageEditJSONFromCodexBecomesSSE(t *testing.T) {
	t.Skip("bug tro-image-generation-output-handling: Codex JSON image edits are returned to SSE clients as JSON")
	var (
		mu      sync.Mutex
		bodies  []map[string]any
		accepts []string
	)
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/images/edits" {
			t.Errorf("upstream got %s %s, want POST /images/edits", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading upstream request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("upstream request is not JSON: %v: %s", err, raw)
		}
		mu.Lock()
		bodies = append(bodies, body)
		accepts = append(accepts, r.Header.Get("Accept"))
		mu.Unlock()

		// The native endpoint ignores stream=true and returns one ordinary Images API body.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, `{"created":1713833628,"data":[{"b64_json":"aW1hZ2U=","revised_prompt":"an edited cat"}],"output_format":"png","size":"1024x1024","quality":"high","usage":{"input_tokens":10,"output_tokens":100,"total_tokens":110}}`)
	}))
	t.Cleanup(u.Close)

	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "codex-image",
				"settings":   map[string]any{"base_url": u.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	})

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	for name, value := range map[string]string{
		"model":  "gpt-image-2",
		"prompt": "put a hat on the cat",
		"stream": "true",
	} {
		if err := mw.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="image[]"; filename="cat.png"`},
		"Content-Type":        {"image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\nimage")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/v1/images/edits", &form)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream: %s", ct, raw)
	}
	if !strings.Contains(string(raw), "event: image_edit.completed\n") || !strings.Contains(string(raw), `"b64_json":"aW1hZ2U="`) {
		t.Fatalf("stream has no completed image event: %s", raw)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("upstream got %d image edit requests, want 1", len(bodies))
	}
	if got := accepts[0]; got != "text/event-stream" {
		t.Errorf("upstream Accept = %q, want text/event-stream", got)
	}
	if stream, _ := bodies[0]["stream"].(bool); !stream {
		t.Errorf("upstream body did not keep stream=true: %v", bodies[0])
	}
	images, _ := bodies[0]["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("upstream body images = %v, want the uploaded image", bodies[0]["images"])
	}
	image, _ := images[0].(map[string]any)
	if url, _ := image["image_url"].(string); !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("uploaded image = %v, want a PNG data URL", image)
	}
}
