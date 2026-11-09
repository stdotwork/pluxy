package corpus

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"
)

// Some of today's core's providers call a fixed upstream host that no setting moves, such as
// the ChatGPT backend a Codex subscription uses, or the OpenAI API a Codex subscription's
// Sign in with ChatGPT login uses. To run such a provider against the fake
// upstream, the corpus gives it an HTTP proxy that accepts CONNECT only to the hosts listed in
// InterceptHosts, ends the TLS connection itself with a certificate from a test authority made
// for this run, and hands the requests inside to the fake upstream. The core under test trusts
// that authority through SSL_CERT_FILE. A CONNECT to any other host is refused, so nothing
// reaches the internet. A plain request for the fake upstream's own address is passed to it.

// InterceptHosts are the hosts (host:port) the intercepting proxy serves.
var InterceptHosts = []string{"chatgpt.com:443", "api.openai.com:443", "aiplatform.googleapis.com:443", "oauth2.googleapis.com:443"}

// testCA is the run's certificate authority; its certificate is written to CAFile.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// File is the PEM file of the authority's certificate, for SSL_CERT_FILE.
	File string

	mu    sync.Mutex
	certs map[string]*tls.Certificate
}

var (
	caOnce sync.Once
	ca     *testCA
	caErr  error
)

// TestCA returns the run's certificate authority, made on first use with its certificate in
// dir.
func TestCA(dir string) (*testCA, error) {
	caOnce.Do(func() { ca, caErr = newTestCA(dir) })
	return ca, caErr
}

func newTestCA(dir string) (*testCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "pluxy corpus test authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	file := dir + string(os.PathSeparator) + "corpus-ca.pem"
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	return &testCA{cert: cert, key: key, File: file, certs: map[string]*tls.Certificate{}}, nil
}

// leaf returns a certificate for host signed by the authority.
func (c *testCA) leaf(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if crt, ok := c.certs[host]; ok {
		return crt, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	crt := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	c.certs[host] = crt
	return crt, nil
}

// interceptor is an HTTP proxy in front of one fake upstream.
type interceptor struct {
	*httptest.Server
	up *FakeUpstream
	ca *testCA

	mu      sync.Mutex
	refused []string // CONNECT targets it refused
}

// Intercept starts an intercepting proxy for u. Its URL goes in the core's proxy setting.
func (u *FakeUpstream) Intercept(ca *testCA) *interceptor {
	ic := &interceptor{up: u, ca: ca}
	ic.Server = httptest.NewServer(http.HandlerFunc(ic.serve))
	return ic
}

// Refused returns the CONNECT targets the proxy refused.
func (ic *interceptor) Refused() []string {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return append([]string(nil), ic.refused...)
}

func (ic *interceptor) serve(w http.ResponseWriter, r *http.Request) {
	// A plain request for the fake upstream itself (the old core's management api-call sends a
	// quota request to an absolute URL on the fake upstream through the account's proxy) goes
	// straight to it.
	if r.Method != http.MethodConnect && r.Host == ic.up.Listener.Addr().String() {
		ic.up.serve(w, r)
		return
	}
	allowed := false
	for _, h := range InterceptHosts {
		allowed = allowed || r.Host == h
	}
	if r.Method != http.MethodConnect || !allowed {
		ic.mu.Lock()
		ic.refused = append(ic.refused, r.Method+" "+r.Host)
		ic.mu.Unlock()
		http.Error(w, "corpus: the intercepting proxy serves only "+fmt.Sprint(InterceptHosts), http.StatusForbidden)
		return
	}
	host, _, _ := net.SplitHostPort(r.Host)
	crt, err := ic.ca.leaf(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		conn.Close()
		return
	}
	var raw net.Conn = conn
	if buf.Reader.Buffered() > 0 {
		raw = &bufferedConn{Conn: conn, r: buf.Reader}
	}
	tc := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{*crt}, NextProtos: []string{"http/1.1"}})
	srv := &http.Server{Handler: http.HandlerFunc(ic.up.serve), ReadHeaderTimeout: 10 * time.Second}
	_ = srv.Serve(newOneConnListener(tc))
}

// bufferedConn reads first what the hijacked connection had already buffered.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// oneConnListener hands out one connection, then blocks until that connection closes.
type oneConnListener struct {
	conn   net.Conn
	once   sync.Once
	closed chan struct{}
}

func newOneConnListener(c net.Conn) *oneConnListener {
	l := &oneConnListener{closed: make(chan struct{})}
	l.conn = &closeNotifyConn{Conn: c, onClose: func() { l.once.Do(func() { close(l.closed) }) }}
	return l
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if c := l.conn; c != nil {
		l.conn = nil
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return &net.TCPAddr{} }

type closeNotifyConn struct {
	net.Conn
	onClose func()
}

func (c *closeNotifyConn) Close() error {
	err := c.Conn.Close()
	c.onClose()
	return err
}
