package sdk

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A tracked HTTP/2 request never shares a connection with another tracked request, and nothing
// it opened stays open: not after a tracked request that was cancelled before it was written,
// and not while two run at once.
func TestTrackSendOwnConnection(t *testing.T) {
	t.Run("after a request cancelled before it was written", func(t *testing.T) {
		target := startH2Target(t)
		client := &http.Client{Transport: Upstream(target.base), Timeout: 10 * time.Second}
		// Cancelling as the request gets its connection races with writing it, so a request is
		// cancelled before it was written about half the time: twenty tries make it all but
		// certain that some were.
		for i := range 20 {
			ctx, cancel := context.WithCancel(context.Background())
			ctx = onGotConn(ctx, func(httptrace.GotConnInfo) { cancel() })
			req, _ := TrackSend(post(t, target.URL+"/token").WithContext(ctx))
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
			cancel()

			// A request can get a connection more than once when it is retried on another.
			var reused bool
			ctx = onGotConn(context.Background(), func(info httptrace.GotConnInfo) { reused = reused || info.Reused })
			req, _ = TrackSend(post(t, target.URL+"/token").WithContext(ctx))
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("try %d: a tracked request after a cancelled one failed: %v", i, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if reused {
				t.Fatalf("try %d: a tracked request reused the connection of a cancelled tracked request", i)
			}
		}
		target.waitAllClosed(t)
	})
	t.Run("two at once", func(t *testing.T) {
		target := startH2Target(t)
		client := &http.Client{Transport: Upstream(target.base), Timeout: 10 * time.Second}
		type result struct {
			reused bool
			err    error
		}
		second := make(chan result, 1)
		secondGotConn := make(chan struct{})
		var once, secondOnce sync.Once
		// The second request starts when the first has its connection and has not yet been
		// written, and the first goes on once the second has its connection too.
		ctx := onGotConn(context.Background(), func(httptrace.GotConnInfo) {
			once.Do(func() {
				go func() {
					var reused bool
					ctx := onGotConn(context.Background(), func(info httptrace.GotConnInfo) {
						reused = reused || info.Reused
						secondOnce.Do(func() { close(secondGotConn) })
					})
					req, _ := TrackSend(post(t, target.URL+"/second").WithContext(ctx))
					resp, err := client.Do(req)
					if err == nil {
						_, _ = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
					second <- result{reused, err}
				}()
				select {
				case <-secondGotConn:
				case <-time.After(10 * time.Second):
					t.Error("the second request got no connection")
				}
			})
		})
		req, _ := TrackSend(post(t, target.URL+"/first").WithContext(ctx))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		r := <-second
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.reused {
			t.Error("the second tracked request reused the first one's connection")
		}
		if first, second := target.connOf("/first"), target.connOf("/second"); first == second {
			t.Errorf("both tracked requests came on connection %s", first)
		}
		target.waitAllClosed(t)
	})
}

// A request through Upstream, tracked or not, directly or through the account's proxy, speaks the
// HTTP version the base transport would: HTTP/2 when net/http turns it on for base by itself, and
// HTTP/1.1 when base turned it off or has a TLS config of its own.
func TestUpstreamKeepsBaseProtocol(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	pass := rand.Text()
	proxies := map[string]string{"directly": ""}
	for name, start := range map[string]func(*testing.T, string) (string, string, func() int){
		"through a connect proxy": startHTTPProxy, "through a socks5 proxy": startSOCKS5Proxy,
	} {
		scheme, addr, _ := start(t, pass)
		proxies[name] = scheme + "://alice:" + pass + "@" + addr
	}

	for _, c := range []struct {
		name string
		base func() *http.Transport
		want int
	}{
		{"a zero transport", func() *http.Transport {
			// net/http gives a zero transport its TLS config, offering h2, on first use (here
			// Clone); the test then trusts the server's certificate in it.
			base := &http.Transport{}
			_ = base.Clone()
			base.TLSClientConfig.RootCAs = roots
			return base
		}, 2},
		{"HTTP/2 turned off with an empty TLSNextProto", func() *http.Transport {
			return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots},
				TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}
		}, 1},
		{"a TLS config of its own", func() *http.Transport {
			return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
		}, 1},
	} {
		for via, proxy := range proxies {
			t.Run(c.name+" "+via, func(t *testing.T) {
				client := &http.Client{Transport: Upstream(c.base()), Timeout: 10 * time.Second}
				ctx, _ := withUpstreamInfo(context.Background(), protocol.Call{Type: protocol.TypeCall, ID: "r", Op: protocol.OpRefresh,
					Account: &protocol.Account{ID: "a", ProxyURL: proxy}})
				for _, track := range []bool{false, true} {
					req := post(t, srv.URL+"/token").WithContext(ctx)
					sent := func() bool { return false }
					if track {
						req, sent = TrackSend(req)
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatalf("tracked %v: %v (counted as sent: %v)", track, err, sent())
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.ProtoMajor != c.want {
						t.Errorf("tracked %v: protocol = %s, want HTTP/%d", track, resp.Proto, c.want)
					}
				}
			})
		}
	}
}

// A tracked request of a call whose account has a proxy goes through that proxy, on a new
// connection each time, while untracked requests through it share one; the client keeps one
// transport for the proxy, and none for tracked requests. A proxy that refuses the request before
// it is written leaves it not sent.
func TestTrackSendThroughProxy(t *testing.T) {
	pass := rand.Text()
	for _, c := range []struct {
		name string
		// start starts the proxy, which wants user alice with password, and returns its URL
		// scheme and address, and a function that reports how many connections the client
		// opened to it so far.
		start func(t *testing.T, password string) (scheme, addr string, conns func() int)
		// https sends to an HTTPS target that speaks HTTP/2, through CONNECT or SOCKS5.
		https bool
	}{
		{"http proxy, http target", startHTTPProxy, false},
		{"connect proxy with a password, http/2 target", startHTTPProxy, true},
		{"socks5 proxy with a password, http/2 target", startSOCKS5Proxy, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var target string
			base := &http.Transport{}
			if c.https {
				h2 := startH2Target(t)
				target, base = h2.URL+"/token", h2.base
			} else {
				// A plain http request goes to the proxy whole, which answers it itself.
				target = "http://login.pluxy-test.invalid/token"
			}
			scheme, addr, conns := c.start(t, pass)
			proxy := scheme + "://alice:" + pass + "@" + addr
			client := &http.Client{Transport: Upstream(base), Timeout: 10 * time.Second}
			call := func(proxy string) context.Context {
				ctx, _ := withUpstreamInfo(context.Background(), protocol.Call{Type: protocol.TypeCall, ID: "r", Op: protocol.OpRefresh,
					Account: &protocol.Account{ID: "a", ProxyURL: proxy}})
				return ctx
			}
			send := func(track bool) {
				t.Helper()
				req := post(t, target).WithContext(call(proxy))
				if track {
					req, _ = TrackSend(req)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want 200", resp.StatusCode)
				}
				if c.https && resp.ProtoMajor != 2 {
					t.Fatalf("protocol = %s, want HTTP/2", resp.Proto)
				}
			}
			send(false)
			send(false)
			if n := conns(); n != 1 {
				t.Errorf("untracked requests opened %d connections to the proxy, want 1 shared", n)
			}
			send(true)
			send(true)
			if n := conns(); n != 3 {
				t.Errorf("two tracked requests opened %d connections to the proxy, want a new one each", n-1)
			}
			if n := len(client.Transport.(upstreamTransport).proxies.by); n != 1 {
				t.Errorf("the client keeps %d transports, want 1, for the proxy", n)
			}

			wrong := scheme + "://alice:wrong-" + pass + "@" + addr
			req, sent := TrackSend(post(t, target).WithContext(call(wrong)))
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					t.Fatal("a proxy that refuses the password passed the request")
				}
				// An http proxy refuses a plain request with its own answer, after reading it.
				if !sent() {
					t.Error("a request the proxy answered counted as not sent")
				}
				return
			}
			if sent() {
				t.Errorf("a request the proxy refused before it was written counted as sent: %v", err)
			}
		})
	}
}

// h2Target is an HTTPS server that speaks HTTP/2 and keeps track of its connections.
type h2Target struct {
	*httptest.Server
	base *http.Transport // a transport that trusts the server and attempts HTTP/2

	mu      sync.Mutex
	open    int
	changed chan struct{} // closed and replaced when open changes
	conns   map[string]string
}

func startH2Target(t *testing.T) *h2Target {
	t.Helper()
	h := &h2Target{changed: make(chan struct{}), conns: map[string]string{}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		h.mu.Lock()
		h.conns[r.URL.Path] = r.RemoteAddr
		h.mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch s {
		case http.StateNew:
			h.open++
		case http.StateClosed, http.StateHijacked:
			h.open--
		default:
			return
		}
		close(h.changed)
		h.changed = make(chan struct{})
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	h.Server = srv
	h.base = srv.Client().Transport.(*http.Transport).Clone()
	h.base.ForceAttemptHTTP2 = true
	return h
}

// connOf returns the connection the last request for path came on.
func (h *h2Target) connOf(path string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[path]
}

// waitAllClosed waits until every connection to the server is closed.
func (h *h2Target) waitAllClosed(t *testing.T) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		h.mu.Lock()
		open, changed := h.open, h.changed
		h.mu.Unlock()
		if open == 0 {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("%d connections to the server are still open", open)
		}
	}
}

// onGotConn returns ctx with a trace that calls f when a request made with it gets its
// connection.
func onGotConn(ctx context.Context, f func(httptrace.GotConnInfo)) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: f})
}

// startHTTPProxy starts an http proxy that wants user alice with password: it tunnels CONNECT and
// answers a plain request itself.
func startHTTPProxy(t *testing.T, password string) (string, string, func() int) {
	t.Helper()
	var mu sync.Mutex
	conns := map[string]bool{}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+password))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns[r.RemoteAddr] = true
		mu.Unlock()
		if r.Header.Get("Proxy-Authorization") != want {
			w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if r.Method != http.MethodConnect {
			_, _ = io.Copy(io.Discard, r.Body)
			return
		}
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		down, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			up.Close()
			return
		}
		_, _ = down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		pipe(down, up)
	}))
	t.Cleanup(srv.Close)
	return "http", srv.Listener.Addr().String(), func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(conns)
	}
}

// startSOCKS5Proxy starts a SOCKS5 proxy that wants user alice with password (RFC 1928 and
// RFC 1929, CONNECT only).
func startSOCKS5Proxy(t *testing.T, password string) (string, string, func() int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var mu sync.Mutex
	conns := 0
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns++
			mu.Unlock()
			go serveSOCKS5(c, password)
		}
	}()
	return "socks5", l.Addr().String(), func() int {
		mu.Lock()
		defer mu.Unlock()
		return conns
	}
}

func serveSOCKS5(c net.Conn, password string) {
	r := bufio.NewReader(c)
	fail := func() { c.Close() }
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil || head[0] != 5 {
		fail()
		return
	}
	if _, err := io.ReadFull(r, make([]byte, head[1])); err != nil {
		fail()
		return
	}
	_, _ = c.Write([]byte{5, 2}) // username and password
	readField := func() string {
		n, err := r.ReadByte()
		if err != nil {
			return ""
		}
		b := make([]byte, n)
		_, _ = io.ReadFull(r, b)
		return string(b)
	}
	if v, err := r.ReadByte(); err != nil || v != 1 {
		fail()
		return
	}
	user, pass := readField(), readField()
	if user != "alice" || pass != password {
		_, _ = c.Write([]byte{1, 1})
		fail()
		return
	}
	_, _ = c.Write([]byte{1, 0})
	var req [4]byte
	if _, err := io.ReadFull(r, req[:]); err != nil || req[1] != 1 {
		fail()
		return
	}
	var host string
	switch req[3] {
	case 1:
		var ip [4]byte
		_, _ = io.ReadFull(r, ip[:])
		host = net.IP(ip[:]).String()
	case 3:
		host = readField()
	case 4:
		var ip [16]byte
		_, _ = io.ReadFull(r, ip[:])
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
	up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))))
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		fail()
		return
	}
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	pipe(&bufferedConn{c, r}, up)
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// pipe copies between a and b until either side ends, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
}
