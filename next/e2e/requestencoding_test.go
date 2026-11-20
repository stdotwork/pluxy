package e2e

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestGzipAndIdentityRequestBodies(t *testing.T) {
	u := newUpstream(t)
	var encodings []string
	var encodingsMu sync.Mutex
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		encodingsMu.Lock()
		encodings = append(encodings, r.Header.Get("Content-Encoding"))
		encodingsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-gzip","object":"chat.completion","created":1700000002,"model":"gpt-test-upstream",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
	base := startCore(t, u)

	body := `{"model":"gpt-test","messages":[{"role":"user","content":"compressed"}]}`
	for _, c := range []struct {
		name, encoding string
		payload        []byte
	}{
		{"gzip", "gzip", gzipRequestBody(t, body)},
		{"identity", "identity", []byte(body)},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := postEncoded(t, context.Background(), base, clientKey, c.payload, c.encoding)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200: %s", resp.StatusCode, raw)
			}
		})
	}

	bodies, _ := u.received()
	if len(bodies) != 2 {
		t.Fatalf("upstream got %d requests, want 2", len(bodies))
	}
	encodingsMu.Lock()
	defer encodingsMu.Unlock()
	for i, got := range bodies {
		if string(got) != body {
			t.Errorf("upstream request %d body = %q, want decoded request %q", i+1, got, body)
		}
		if encodings[i] != "" {
			t.Errorf("upstream request %d Content-Encoding = %q, want none", i+1, encodings[i])
		}
	}
}

func TestUnsupportedRequestContentEncoding(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(http.ResponseWriter, *http.Request, []byte) {
		t.Error("the upstream was called")
	}
	base := startCore(t, u)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`

	for _, encoding := range []string{"zstd", "br", "compress"} {
		t.Run(encoding, func(t *testing.T) {
			resp := postEncoded(t, context.Background(), base, clientKey, []byte(body), encoding)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("status %d, want 415: %s", resp.StatusCode, raw)
			}
			if !strings.Contains(string(raw), encoding) {
				t.Errorf("reply does not name %q: %s", encoding, raw)
			}
		})
	}

	if bodies, _ := u.received(); len(bodies) != 0 {
		t.Errorf("upstream got %d requests, want none", len(bodies))
	}
}

func TestGzipRequestBodyForEndpoint(t *testing.T) {
	const reply = `{"id":"v1_gzip","status":"completed","model":"gemini-2.5-flash","outputs":[{"type":"text","text":"ok"}]}`
	up := newFakeGemini(t, jsonReply(reply))
	base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
	body := `{"model":"gemini-2.5-flash","input":"compressed endpoint request"}`

	resp := postEncodedPath(t, context.Background(), base, interactionsPath, clientKey, gzipRequestBody(t, body), "gzip")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(raw) != reply {
		t.Fatalf("status %d: %s; want 200 with the endpoint reply", resp.StatusCode, raw)
	}
	got := up.interactions()
	if len(got) != 1 {
		t.Fatalf("upstream got %d Interactions requests (%s), want 1", len(got), up.all())
	}
	if input := string(bodyFields(t, got[0].body)["input"]); input != `"compressed endpoint request"` {
		t.Errorf("upstream input = %s, want the decoded request body", input)
	}
	if encoding := got[0].header.Get("Content-Encoding"); encoding != "" {
		t.Errorf("upstream Content-Encoding = %q, want none", encoding)
	}
}

func TestGzipRequestBodyForTokenCount(t *testing.T) {
	up := newFakeGemini(t, jsonReply(`{"totalTokens":7}`))
	base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
	body := `{"contents":[{"role":"user","parts":[{"text":"count this compressed request"}]}]}`

	resp := postEncodedPath(t, context.Background(), base, "/v1beta/models/gemini-2.5-flash:countTokens", clientKey,
		gzipRequestBody(t, body), "gzip")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"totalTokens":7`) {
		t.Fatalf("status %d: %s; want 200 with the upstream count", resp.StatusCode, raw)
	}
	up.mu.Lock()
	seen := append([]seenRequest(nil), up.seen...)
	up.mu.Unlock()
	var counted []seenRequest
	for _, req := range seen {
		if strings.HasSuffix(req.path, ":countTokens") {
			counted = append(counted, req)
		}
	}
	if len(counted) != 1 {
		t.Fatalf("upstream got %d countTokens requests (%s), want 1", len(counted), up.all())
	}
	if string(counted[0].body) != body {
		t.Errorf("upstream body = %q, want decoded request %q", counted[0].body, body)
	}
	if encoding := counted[0].header.Get("Content-Encoding"); encoding != "" {
		t.Errorf("upstream Content-Encoding = %q, want none", encoding)
	}
}

func TestGzipRequestBodyOverDecodedLimit(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(http.ResponseWriter, *http.Request, []byte) {
		t.Error("the upstream was called")
	}
	base := startCore(t, u)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"` + strings.Repeat("a", 24<<20) + `"}]}`
	compressed := gzipRequestBody(t, body)
	if len(compressed) >= 1<<20 {
		t.Fatalf("compressed request is %d bytes; the test needs a small payload under a large decoded body", len(compressed))
	}

	resp := postEncoded(t, context.Background(), base, clientKey, compressed, "gzip")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413: %s", resp.StatusCode, raw)
	}
	if bodies, _ := u.received(); len(bodies) != 0 {
		t.Errorf("upstream got %d requests, want none", len(bodies))
	}
}

func gzipRequestBody(t *testing.T, body string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := io.WriteString(zw, body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func postEncoded(t *testing.T, ctx context.Context, base, key string, payload []byte, encoding string) *http.Response {
	t.Helper()
	return postEncodedPath(t, ctx, base, "/v1/chat/completions", key, payload, encoding)
}

func postEncodedPath(t *testing.T, ctx context.Context, base, path, key string, payload []byte, encoding string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", encoding)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(fmt.Errorf("POST %s as %s: %w", base, encoding, err))
	}
	return resp
}
