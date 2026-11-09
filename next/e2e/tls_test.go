package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCert is a self-signed certificate for 127.0.0.1 and localhost, made for one test.
type testCert struct {
	certPEM, keyPEM []byte
	pool            *x509.CertPool
}

func newTestCert(t *testing.T, cn string) testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return testCert{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}),
		pool:    pool,
	}
}

func (c testCert) write(t *testing.T, certPath, keyPath string) {
	t.Helper()
	if err := os.WriteFile(certPath, c.certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, c.keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

// tlsClient trusts only pool and opens a new connection for every request.
func tlsClient(pool *x509.CertPool) *http.Client {
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool},
		ForceAttemptHTTP2: true,
		DisableKeepAlives: true,
	}, Timeout: 10 * time.Second}
}

// Core serves its client API and management API over HTTPS with HTTP/2 when tls.enable is on,
// serves a renewed certificate written over the same files without a restart, refuses plain
// HTTP on that port, and refuses a reload that changes the tls section.
func TestTLSListener(t *testing.T) {
	u := newUpstream(t)
	u.respond = u.streamReply
	close(u.release) // stream the whole reply at once
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	first := newTestCert(t, "pluxy-first")
	first.write(t, certPath, keyPath)
	cfg := map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"tls":            map[string]any{"enable": true, "cert": certPath, "key": keyPath},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "upstream-1",
				"settings":   map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	}
	base, path, _ := runCoreArgs(t, cfg, noWatch...)
	if !strings.HasPrefix(base, "https://") {
		t.Fatalf("core reported %s, want an https:// address", base)
	}

	do := func(client *http.Client, method, path, key, body string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	client := tlsClient(first.pool)
	resp, body := do(client, http.MethodPost, "/v1/chat/completions", clientKey, `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "world!") {
		t.Fatalf("chat over TLS: %d %s", resp.StatusCode, body)
	}
	if resp.ProtoMajor != 2 || resp.TLS == nil || resp.TLS.NegotiatedProtocol != "h2" {
		t.Errorf("chat over TLS used %s (ALPN %q), want HTTP/2", resp.Proto, resp.TLS.NegotiatedProtocol)
	}
	if resp, body := do(client, http.MethodGet, "/v0/management/status", managementKey, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("management over TLS: %d %s", resp.StatusCode, body)
	}

	// Plain HTTP on the TLS port gets no reply from the API.
	plain := "http://" + strings.TrimPrefix(base, "https://")
	if resp, err := http.Get(plain + "/v1/models"); err != nil {
		t.Errorf("plain HTTP: %v, want Go's 400 for HTTP on an HTTPS port", err)
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "HTTPS") {
			t.Errorf("plain HTTP answered %d %s, want Go's 400 for HTTP on an HTTPS port", resp.StatusCode, b)
		}
	}

	// A certificate written over the same files is served once core sees the change.
	renewed := newTestCert(t, "pluxy-renewed")
	renewed.write(t, certPath, keyPath)
	both := x509.NewCertPool()
	both.AppendCertsFromPEM(first.certPEM)
	both.AppendCertsFromPEM(renewed.certPEM)
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, _ := do(tlsClient(both), http.MethodGet, "/v1/models", clientKey, "")
		if cn := resp.TLS.PeerCertificates[0].Subject.CommonName; cn == "pluxy-renewed" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("core still serves %q 10 s after the renewal", cn)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if resp, body := do(tlsClient(renewed.pool), http.MethodGet, "/v1/models", clientKey, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("models with the renewed certificate: %d %s", resp.StatusCode, body)
	}

	// Turning TLS off needs a restart: the reload is refused and HTTPS keeps serving.
	cfg["tls"] = map[string]any{}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	resp, body = do(tlsClient(renewed.pool), http.MethodPost, "/v0/management/reload", managementKey, "")
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "tls cannot change") {
		t.Errorf("reload turning TLS off: %d %s, want 422 naming tls", resp.StatusCode, body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+clientKey)
	if resp, err := tlsClient(renewed.pool).Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("after the refused reload: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
}

// Core does not start when tls.enable is on and the files do not load.
func TestTLSListenerNeedsLoadableFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"tls":            map[string]any{"enable": true, "cert": filepath.Join(dir, "missing.pem"), "key": filepath.Join(dir, "missing-key.pem")},
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runToExit(t, path)
	if err == nil || !strings.Contains(out, "tls:") || strings.Contains(out, "listening on") {
		t.Errorf("core exited with %v:\n%s\nwant a failure naming tls, before listening", err, out)
	}
}

// runToExit runs core with the config file at path and returns its output once it exits.
func runToExit(t *testing.T, path string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(binDir, "pluxy"), "-config", path, "-watch-interval", "0").CombinedOutput()
	return string(out), err
}
