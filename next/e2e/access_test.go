package e2e

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// accessConfig is a config for core in front of u, with the management API on.
func accessConfig(u *upstream) map[string]any {
	return map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "upstream-1",
				"settings":   map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	}
}

// postFrom sends a chat request with the given X-Forwarded-For and returns the status.
func postFrom(t *testing.T, base, forwardedFor string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", forwardedFor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// The client address in core's log lines is the one a trusted proxy names in X-Forwarded-For,
// and the connection's own address for anyone else; a reload changes the trusted proxies.
func TestTrustedProxies(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"bad input","type":"invalid_request_error"}}`)
	}
	cfg := accessConfig(u)
	cfg["trusted_proxies"] = []string{"127.0.0.1"}
	base, path, logs := runCoreArgs(t, cfg, noWatch...)

	// The rightmost address that is not a trusted proxy is the client; the left one is spoofable.
	if code := postFrom(t, base, "203.0.113.50, 198.51.100.7, 127.0.0.1"); code != http.StatusBadRequest {
		t.Fatalf("request answered %d, want the upstream's 400", code)
	}
	logs.waitFor(t, "POST /v1/chat/completions from 198.51.100.7 [trace")

	delete(cfg, "trusted_proxies")
	writeConfig(t, path, cfg)
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: %d %+v", code, s)
	}
	postFrom(t, base, "198.51.100.8")
	all := logs.waitFor(t, "POST /v1/chat/completions from 127.0.0.1 [trace")
	if strings.Contains(all, "198.51.100.8") {
		t.Errorf("a header from a peer that is not a trusted proxy was used:\n%s", all)
	}

	cfg["trusted_proxies"] = []string{"not-an-address"}
	writeConfig(t, path, cfg)
	if code, s := reload(t, base); code != http.StatusUnprocessableEntity || !strings.Contains(s.Desired.Error, "trusted_proxies[0]") {
		t.Errorf("reload with a bad trusted proxy: %d, error %q; want 422 naming trusted_proxies[0]", code, s.Desired.Error)
	}
}
