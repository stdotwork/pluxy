package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An Images API generation on a Gemini image model goes through core to the Gemini extension as
// one generateContent call, and each image Gemini returns reaches the client as b64_json.
func TestGeminiImagesGeneration(t *testing.T) {
	const image = "aW1hZ2UtYXBp"
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"`+image+`"}}]},"finishReason":"STOP","index":0}],`+
			`"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18},"modelVersion":"gemini-test"}`)
	})
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{map[string]any{
				"id":         "gemini-image",
				"models":     []string{"gemini-3.1-flash-image"},
				"settings":   map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-gemini-image-key"},
			}},
		},
	})
	resp := postPath(t, context.Background(), base, "/v1/images/generations", clientKey,
		`{"model":"gemini-3.1-flash-image","prompt":"draw a cat","size":"1536x1024","n":1,"response_format":"b64_json"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var reply struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || len(reply.Data) != 1 || reply.Data[0].B64JSON != image {
		t.Fatalf("reply %s: %+v, err %v", raw, reply, err)
	}
	up.mu.Lock()
	allSeen := append([]seenRequest(nil), up.seen...)
	up.mu.Unlock()
	var seen []seenRequest
	for _, req := range allSeen {
		if req.method == http.MethodPost {
			seen = append(seen, req)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("upstream got %d generation requests, want 1: %s", len(seen), up.all())
	}
	if seen[0].method != http.MethodPost || seen[0].path != "/v1beta/models/gemini-3.1-flash-image:generateContent" {
		t.Errorf("upstream request = %s %s", seen[0].method, seen[0].path)
	}
	var sent struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseModalities []string `json:"responseModalities"`
			ImageConfig        struct {
				AspectRatio string `json:"aspectRatio"`
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(seen[0].body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Contents) != 1 || len(sent.Contents[0].Parts) != 1 || sent.Contents[0].Parts[0].Text != "draw a cat" ||
		strings.Join(sent.GenerationConfig.ResponseModalities, ",") != "IMAGE" || sent.GenerationConfig.ImageConfig.AspectRatio != "3:2" {
		t.Errorf("upstream body = %s", seen[0].body)
	}
}
