package e2e

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// proxiedHost is a host name only the fake proxies know: they send it to 127.0.0.1. An upstream
// URL with it is reached through a proxy or not at all.
const proxiedHost = "upstream.test"

// proxyUser and proxyPassword are the user name and password the fake proxies ask for when they
// are made with auth. Both are distinctive, so a test can look for either anywhere.
const (
	proxyUser     = "pxu-Rk82vq"
	proxyPassword = "pr0xy-Secret-9f3k"
)

// fakeProxy is an HTTP or SOCKS5 proxy that records each request it gets, in its own words
// ("GET http://upstream.test:1234/v1/x", "CONNECT upstream.test:443", "SOCKS upstream.test:1234"),
// and the local address of each connection it opens to a target, so a test can tell the
// upstream's connections that came through it.
type fakeProxy struct {
	addr string
	auth bool // ask for proxyUser and proxyPassword

	mu       sync.Mutex
	requests []string
	creds    []string // the user name and password each request came with, "" for none
	out      map[string]bool
	bodyEnd  func() // see holdBodyEnd
}

// holdBodyEnd makes the HTTP proxy call f before it reads a forwarded request body again after
// the body ended, which net/http does to check nothing follows: a test holds that read back with
// f, as a busy machine may.
func (p *fakeProxy) holdBodyEnd(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodyEnd = f
}

// lateEnd is a forwarded request body that calls end before each read after the body ended.
type lateEnd struct {
	io.ReadCloser
	end   func()
	ended bool
}

func (b *lateEnd) Read(buf []byte) (int, error) {
	if b.ended {
		b.end()
	}
	n, err := b.ReadCloser.Read(buf)
	b.ended = b.ended || err == io.EOF
	return n, err
}

// URL returns the proxy's URL with scheme, and with its user name and password when it asks
// for them.
func (p *fakeProxy) URL(scheme string) string {
	if p.auth {
		return scheme + "://" + url.UserPassword(proxyUser, proxyPassword).String() + "@" + p.addr
	}
	return scheme + "://" + p.addr
}

func (p *fakeProxy) record(req, cred string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	p.creds = append(p.creds, cred)
}

// seen returns the requests the proxy got and the credentials each came with.
func (p *fakeProxy) seen() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...), append([]string(nil), p.creds...)
}

// opened reports whether the proxy opened the connection whose local address is addr.
func (p *fakeProxy) opened(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out[addr]
}

// dial connects to a target: proxiedHost goes to 127.0.0.1, anything else is refused, so a
// test never reaches out.
func (p *fakeProxy) dial(ctx context.Context, hostport string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	if host != proxiedHost && host != "127.0.0.1" {
		return nil, fmt.Errorf("fake proxy: %s is not a test host", host)
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.out == nil {
		p.out = map[string]bool{}
	}
	p.out[c.LocalAddr().String()] = true
	p.mu.Unlock()
	return c, nil
}

// newHTTPProxy starts an HTTP proxy: plain HTTP requests in absolute form are passed on (WebSocket
// upgrades too), CONNECT opens a tunnel.
func newHTTPProxy(t *testing.T, auth bool) *fakeProxy {
	t.Helper()
	p := &fakeProxy{auth: auth}
	forward := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL = r.In.URL
			r.Out.Host = r.In.Host
			p.mu.Lock()
			end := p.bodyEnd
			p.mu.Unlock()
			if end != nil && r.Out.Body != nil {
				r.Out.Body = &lateEnd{ReadCloser: r.Out.Body, end: end}
			}
		},
		Transport: &http.Transport{
			DialContext:       func(ctx context.Context, _, addr string) (net.Conn, error) { return p.dial(ctx, addr) },
			DisableKeepAlives: true,
		},
		FlushInterval: -1,
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := ""
		if v, ok := strings.CutPrefix(r.Header.Get("Proxy-Authorization"), "Basic "); ok {
			b, _ := base64.StdEncoding.DecodeString(v)
			cred = string(b)
		}
		if r.Method == http.MethodConnect {
			p.record("CONNECT "+r.Host, cred)
		} else {
			p.record(r.Method+" "+r.URL.String(), cred)
		}
		if p.auth && cred != proxyUser+":"+proxyPassword {
			w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if r.Method != http.MethodConnect {
			if !r.URL.IsAbs() {
				http.Error(w, "fake proxy: not a proxy request", http.StatusBadRequest)
				return
			}
			// The forwarder still reads the request body (to check it ended) after the upstream has
			// all of it and may have answered. By default the server reads and closes that body when
			// the reply's headers go out, and the forwarder's late read then fails, which closes the
			// upstream connection mid-reply. Full duplex leaves the body to the forwarder.
			if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
				http.Error(w, "fake proxy: "+err.Error(), http.StatusInternalServerError)
				return
			}
			forward.ServeHTTP(w, r)
			return
		}
		target, err := p.dial(r.Context(), r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		client, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			target.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		pipe(client, rw.Reader, target)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.addr = ln.Addr().String()
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return p
}

// The HTTP proxy keeps a streamed reply going when it reads the request body past its end only
// after the reply has started, as it may on a busy machine.
func TestHTTPProxyStreamsWhenBodyEndReadLate(t *testing.T) {
	firstSeen := make(chan struct{}) // the client has the reply's first event
	lateRead := make(chan struct{})  // the proxy goes on to read past the body's end
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-lateRead:
		case <-time.After(10 * time.Second):
		}
		// Time for that read, and for whatever it makes the proxy do, before the rest goes.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		_, _ = io.WriteString(w, "data: second\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	p := newHTTPProxy(t, false)
	var once sync.Once
	p.holdBodyEnd(func() {
		select {
		case <-firstSeen:
		case <-time.After(10 * time.Second):
		}
		once.Do(func() { close(lateRead) })
	})
	proxyURL, _ := url.Parse(p.URL("http"))
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	t.Cleanup(tr.CloseIdleConnections)
	resp, err := (&http.Client{Transport: tr}).Post(up.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n')
	if err != nil || first != "data: first\n" {
		t.Fatalf("first line %q, %v", first, err)
	}
	close(firstSeen)
	rest, err := io.ReadAll(br)
	if err != nil || !strings.Contains(string(rest), "data: second\n\ndata: [DONE]\n\n") {
		t.Fatalf("after the first event: %q, %v", rest, err)
	}
}

// pipe copies between client (whose reader may hold bytes read ahead) and target until either
// side ends.
func pipe(client net.Conn, r io.Reader, target net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, r); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
	<-done
	client.Close()
	target.Close()
}

// newSOCKS5Proxy starts a SOCKS5 server (RFC 1928) that takes CONNECT, with no authentication or,
// when auth is set, user name and password (RFC 1929).
func newSOCKS5Proxy(t *testing.T, auth bool) *fakeProxy {
	t.Helper()
	p := &fakeProxy{auth: auth}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serveSOCKS(c)
		}
	}()
	return p
}

func (p *fakeProxy) serveSOCKS(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	fail := func() { c.Close() }
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil || head[0] != 5 {
		fail()
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(r, methods); err != nil {
		fail()
		return
	}
	want := byte(0)
	if p.auth {
		want = 2
	}
	if !strings.ContainsRune(string(methods), rune(want)) {
		_, _ = c.Write([]byte{5, 0xff})
		fail()
		return
	}
	_, _ = c.Write([]byte{5, want})
	cred := ""
	if p.auth {
		user, pass, err := readUserPass(r)
		if err != nil {
			fail()
			return
		}
		cred = user + ":" + pass
		if cred != proxyUser+":"+proxyPassword {
			p.record("SOCKS (refused credentials)", cred)
			_, _ = c.Write([]byte{1, 1})
			fail()
			return
		}
		_, _ = c.Write([]byte{1, 0})
	}
	var req [4]byte
	if _, err := io.ReadFull(r, req[:]); err != nil || req[0] != 5 || req[1] != 1 {
		fail()
		return
	}
	var host string
	switch req[3] {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			fail()
			return
		}
		host = net.IP(ip[:]).String()
	case 3:
		n, err := r.ReadByte()
		if err != nil {
			fail()
			return
		}
		name := make([]byte, n)
		if _, err := io.ReadFull(r, name); err != nil {
			fail()
			return
		}
		host = string(name)
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			fail()
			return
		}
		host = net.IP(ip[:]).String()
	default:
		fail()
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		fail()
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:]))))
	p.record("SOCKS "+target, cred)
	t, err := p.dial(context.Background(), target)
	if err != nil {
		_, _ = c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
		fail()
		return
	}
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	_ = c.SetDeadline(time.Time{})
	pipe(c, r, t)
}

func readUserPass(r *bufio.Reader) (string, string, error) {
	ver, err := r.ReadByte()
	if err != nil || ver != 1 {
		return "", "", errors.New("bad auth version")
	}
	read := func() (string, error) {
		n, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		b := make([]byte, n)
		_, err = io.ReadFull(r, b)
		return string(b), err
	}
	user, err := read()
	if err != nil {
		return "", "", err
	}
	pass, err := read()
	return user, pass, err
}
