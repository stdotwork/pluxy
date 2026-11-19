package e2e

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
)

// A Vertex service account's access token is one the Gemini extension fetches during the call,
// so core never knows it as a secret. The token exchange that fetches it (the signed assertion
// going out, the token coming back) is not an upstream exchange of the call: the request log's
// upstream capture leaves it out, and keeps the model request, whose Authorization header is
// redacted by name.
func TestVertexTokenExchangeIsNotCaptured(t *testing.T) {
	srv := geminitest.New(t)
	key := srv.ServiceAccount("leak-project", true)
	r := runLeak(t, filepath.Join(binDir, "gemini", "pluxy-extension.json"), "gemini-2.5-flash",
		map[string]any{"kind": "service_account", "data": json.RawMessage(key)}, nil,
		map[string]any{"base_url": srv.URL, "vertex": map[string]any{"project": "leak-project", "location": "us-central1"}})
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Kind != "vertex" {
		t.Fatalf("the fake got %d model requests, want one to Vertex: %+v", len(reqs), reqs)
	}
	token := strings.TrimPrefix(reqs[0].Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(token, "geminitest-token-") {
		t.Fatalf("the model request's token is %q", token)
	}
	// The fake's tokens share "geminitest" with the key's private_key_id, which core removes, so the
	// token alone would show only in part here; the exchange's absence is what is checked.
	r.wantNoSecret(t, token)
	for _, s := range []string{"assertion=", "access_token"} {
		if strings.Contains(r.reqLog, s) {
			t.Errorf("the request log holds the token exchange (%q):\n%s", s, r.reqLog)
		}
	}
	if n := strings.Count(r.reqLog, "=== UPSTREAM REQUEST ==="); n != 1 || !strings.Contains(r.reqLog, ":generateContent") {
		t.Errorf("the request log holds %d upstream requests, want only the model request:\n%s", n, r.reqLog)
	}
}
