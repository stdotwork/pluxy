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

// managementFrom calls GET /v0/management/status with key, as the client a trusted proxy names
// in X-Forwarded-For.
func managementFrom(t *testing.T, base, forwardedFor, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/v0/management/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// Five wrong management keys from one client block it for 30 minutes, the right key included,
// while other clients are served; behind a trusted proxy the client is the forwarded address.
func TestManagementLockout(t *testing.T) {
	cfg := accessConfig(newUpstream(t))
	cfg["trusted_proxies"] = []string{"127.0.0.1"}
	cfg["management_allow_remote"] = true // the clients below are other machines
	base, _, logs := runCoreArgs(t, cfg, noWatch...)

	const attacker, owner = "198.51.100.66", "198.51.100.1"
	for i := range 5 {
		if resp := managementFrom(t, base, attacker, "wrong-key"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong key %d answered %d, want 401", i+1, resp.StatusCode)
		}
	}
	logs.waitFor(t, "management: 5 failed management keys in a row from "+attacker)
	resp := managementFrom(t, base, attacker, managementKey)
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Retry-After") == "" {
		t.Errorf("blocked client with the right key answered %d (Retry-After %q), want 403 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if resp := managementFrom(t, base, owner, managementKey); resp.StatusCode != http.StatusOK {
		t.Errorf("another client answered %d, want 200", resp.StatusCode)
	}
	// The proxy itself (no forwarded address) is another client too.
	if resp := managementFrom(t, base, "", managementKey); resp.StatusCode != http.StatusOK {
		t.Errorf("the proxy's own address answered %d, want 200", resp.StatusCode)
	}
}

// The management API answers only this machine unless management_allow_remote is on. A request
// a proxy on this machine forwards from elsewhere is not from this machine: when core does not
// trust the proxy, any forwarding header says so; when it does, the forwarded client decides.
func TestManagementRemoteGate(t *testing.T) {
	cfg := accessConfig(newUpstream(t))
	base, path, _ := runCoreArgs(t, cfg, noWatch...)

	if resp := managementFrom(t, base, "", managementKey); resp.StatusCode != http.StatusOK {
		t.Fatalf("from this machine: %d, want 200", resp.StatusCode)
	}
	if resp := managementFrom(t, base, "198.51.100.9", managementKey); resp.StatusCode != http.StatusForbidden {
		t.Errorf("forwarded by a proxy core does not trust: %d, want 403", resp.StatusCode)
	}

	cfg["trusted_proxies"] = []string{"127.0.0.1"}
	writeConfig(t, path, cfg)
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: %d %+v", code, s)
	}
	if resp := managementFrom(t, base, "198.51.100.9", managementKey); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another machine behind a trusted proxy: %d, want 403", resp.StatusCode)
	}
	if resp := managementFrom(t, base, "::1", managementKey); resp.StatusCode != http.StatusOK {
		t.Errorf("this machine behind a trusted proxy: %d, want 200", resp.StatusCode)
	}
	// The client API is not gated.
	if code := postFrom(t, base, "198.51.100.9"); code == http.StatusForbidden || code == http.StatusUnauthorized {
		t.Errorf("client API from another machine answered %d", code)
	}

	// A remote client's wrong keys are refused by the gate and not counted for the lockout.
	for range 6 {
		managementFrom(t, base, "198.51.100.10", "wrong-key")
	}
	cfg["management_allow_remote"] = true
	writeConfig(t, path, cfg)
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: %d %+v", code, s)
	}
	if resp := managementFrom(t, base, "198.51.100.10", managementKey); resp.StatusCode != http.StatusOK {
		t.Errorf("with management_allow_remote, another machine: %d, want 200", resp.StatusCode)
	}
	if resp := managementFrom(t, base, "198.51.100.11", "wrong-key"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("with management_allow_remote, a wrong key: %d, want 401", resp.StatusCode)
	}
}
