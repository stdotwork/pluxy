package e2e

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// probe sends method to base+"/healthz" with headers, and returns the reply with its body read.
func probe(t *testing.T, client *http.Client, method, base string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, base+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s /healthz: %v", method, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// checkHealthz is what a container or load balancer sends: no key. GET answers 200 with the text
// "ok", HEAD the same status and type with no body, and any other method does not answer 200.
func checkHealthz(t *testing.T, client *http.Client, base string) {
	t.Helper()
	const wantType = "text/plain; charset=utf-8"

	resp, body := probe(t, client, http.MethodGet, base, nil)
	if resp.StatusCode != http.StatusOK || body != "ok" {
		t.Errorf("GET /healthz: %d %q, want 200 \"ok\"", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != wantType {
		t.Errorf("GET /healthz content type %q, want %q", ct, wantType)
	}

	resp, body = probe(t, client, http.MethodHead, base, nil)
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Errorf("HEAD /healthz: %d with a %d-byte body, want 200 and no body", resp.StatusCode, len(body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != wantType {
		t.Errorf("HEAD /healthz content type %q, want %q", ct, wantType)
	}

	resp, body = probe(t, client, http.MethodPost, base, nil)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("POST /healthz answered 200 %q, want anything but 200", body)
	}
}

// The probe needs no client key, and no management key, CORS origin or trusted proxy gates it.
func TestHealthz(t *testing.T) {
	base := startCore(t, newUpstream(t), func(cfg map[string]any) {
		cfg["management_key"] = managementKey // the panel is on, mounted at /panel/
		cfg["trusted_proxies"] = []string{"127.0.0.1"}
		cfg["cors"] = map[string]any{"allowed_origins": []string{"https://app.example.com"}}
	})
	checkHealthz(t, http.DefaultClient, base)

	// Another origin, and a forwarding header from a peer that is not a trusted proxy.
	resp, body := probe(t, http.DefaultClient, http.MethodGet, base, map[string]string{
		"Origin":          "https://other.example",
		"X-Forwarded-For": "198.51.100.9",
	})
	if resp.StatusCode != http.StatusOK || body != "ok" {
		t.Errorf("GET /healthz from another origin: %d %q, want 200 \"ok\"", resp.StatusCode, body)
	}
}

// The probe works over HTTPS too, where core serves its client API with a certificate.
func TestHealthzTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cert := newTestCert(t, "pluxy-healthz")
	cert.write(t, certPath, keyPath)
	base := startCore(t, newUpstream(t), func(cfg map[string]any) {
		cfg["tls"] = map[string]any{"enable": true, "cert": certPath, "key": keyPath}
	})
	if !strings.HasPrefix(base, "https://") {
		t.Fatalf("core reported %s, want an https:// address", base)
	}
	checkHealthz(t, tlsClient(cert.pool), base)
}
