package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSigned returns a new self-signed certificate for localhost named cn, and its key, as PEM.
func selfSigned(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})
}

func served(t *testing.T, c *CertFiles) string {
	t.Helper()
	c.mu.Lock()
	c.checked = time.Time{} // look at the files now
	c.mu.Unlock()
	pair, err := c.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.Subject.CommonName
}

// A changed certificate and key are served from the next handshake; files that do not make a
// pair leave the previous certificate in service.
func TestCertFilesReadAgainWhenChanged(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	write := func(certPEM, keyPEM []byte) {
		t.Helper()
		if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	certA, keyA := selfSigned(t, "first")
	write(certA, keyA)
	c, err := LoadCertFiles(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := served(t, c); got != "first" {
		t.Fatalf("served %q, want first", got)
	}

	certB, keyB := selfSigned(t, "renewed")
	write(certB, keyB)
	if got := served(t, c); got != "renewed" {
		t.Errorf("after renewal served %q, want renewed", got)
	}

	// Half a renewal: the new certificate is written, its key not yet.
	certC, keyC := selfSigned(t, "third")
	if err := os.WriteFile(certPath, certC, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := served(t, c); got != "renewed" {
		t.Errorf("with a certificate that does not match its key, served %q, want renewed", got)
	}
	if err := os.WriteFile(keyPath, keyC, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := served(t, c); got != "third" {
		t.Errorf("once the key matches, served %q, want third", got)
	}
}

func TestLoadCertFilesNeedsAPair(t *testing.T) {
	dir := t.TempDir()
	certPEM, _ := selfSigned(t, "x")
	_, otherKey := selfSigned(t, "y")
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if _, err := LoadCertFiles(certPath, keyPath); err == nil {
		t.Error("missing files loaded")
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, otherKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCertFiles(certPath, keyPath); err == nil {
		t.Error("a certificate with another certificate's key loaded")
	}
}

func TestConfigTLS(t *testing.T) {
	if _, err := ParseConfig([]byte(`{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","tls":{"enable":true,"cert":"c.pem"}}`), "/srv/pluxy/config.json"); err == nil || !strings.Contains(err.Error(), "tls.key") {
		t.Errorf("tls without a key: ParseConfig = %v", err)
	}
	c, err := ParseConfig([]byte(`{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","tls":{"enable":true,"cert":" certs/c.pem","key":"/etc/k.pem "}}`), "/srv/pluxy/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.TLS.Cert != "/srv/pluxy/certs/c.pem" || c.TLS.Key != "/etc/k.pem" {
		t.Errorf("tls = %+v, want the paths trimmed and the cert relative to the config file", c.TLS)
	}
	c, err = ParseConfig([]byte(`{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","tls":{"cert":"c.pem"}}`), "/srv/pluxy/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if tc, err := c.TLSConfig(); tc != nil || err != nil {
		t.Errorf("tls off: TLSConfig = %v, %v; want none", tc, err)
	}
}
