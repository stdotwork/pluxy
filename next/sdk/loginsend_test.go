package sdk

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// post builds a POST with a body, as a token request is.
func post(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader("grant_type=refresh_token"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// trackedDo sends req through client with TrackSend and returns the error and whether the
// request counted as sent.
func trackedDo(t *testing.T, client *http.Client, req *http.Request) (error, bool) {
	t.Helper()
	tracked, sent := TrackSend(req)
	resp, err := client.Do(tracked)
	if err == nil {
		resp.Body.Close()
	}
	return err, sent()
}

func TestTrackSendNotSent(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		err, sent := trackedDo(t, &http.Client{Timeout: 10 * time.Second}, post(t, "http://"+addr+"/token"))
		if err == nil {
			t.Fatal("a closed port answered")
		}
		if sent {
			t.Errorf("a refused connection counted as sent: %v", err)
		}
	})
	t.Run("tls handshake fails", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("the request reached a server whose certificate the client does not trust")
		}))
		defer srv.Close()
		// A plain client does not trust the test certificate.
		err, sent := trackedDo(t, &http.Client{Timeout: 10 * time.Second}, post(t, srv.URL+"/token"))
		if err == nil {
			t.Fatal("an untrusted certificate was accepted")
		}
		if sent {
			t.Errorf("a failed handshake counted as sent: %v", err)
		}
	})
	t.Run("dns lookup fails", func(t *testing.T) {
		// .invalid never resolves (RFC 6761).
		err, sent := trackedDo(t, &http.Client{Timeout: 10 * time.Second}, post(t, "http://login.pluxy-test.invalid/token"))
		if err == nil {
			t.Fatal("a .invalid name resolved")
		}
		if sent {
			t.Errorf("a failed lookup counted as sent: %v", err)
		}
	})
}

func TestTrackSendSent(t *testing.T) {
	t.Run("closed after reading the request", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				buf := make([]byte, 4096)
				var got string
				for !strings.Contains(got, "grant_type") {
					n, err := c.Read(buf)
					if err != nil {
						break
					}
					got += string(buf[:n])
				}
				c.Close()
			}
		}()
		err, sent := trackedDo(t, &http.Client{Timeout: 10 * time.Second}, post(t, "http://"+l.Addr().String()+"/token"))
		if err == nil {
			t.Fatal("a server that hung up answered")
		}
		if !sent {
			t.Errorf("a request the server read counted as not sent: %v", err)
		}
	})
	t.Run("answered 503", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		tracked, sent := TrackSend(post(t, srv.URL+"/token"))
		resp, err := http.DefaultClient.Do(tracked)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
		if !sent() {
			t.Error("an answered request counted as not sent")
		}
	})
	t.Run("http2 answered 503", func(t *testing.T) {
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		srv.EnableHTTP2 = true
		srv.StartTLS()
		defer srv.Close()
		client := srv.Client()
		tracked, sent := TrackSend(post(t, srv.URL+"/token"))
		resp, err := client.Do(tracked)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.ProtoMajor != 2 {
			t.Fatalf("protocol = %s, want HTTP/2", resp.Proto)
		}
		if !sent() {
			t.Error("an answered HTTP/2 request counted as not sent")
		}
	})
}

func TestTrackSendKeepsOtherTraces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	var wrote bool
	req := post(t, srv.URL+"/token")
	ctx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote = true },
	})
	req = req.WithContext(ctx)
	tracked, sent := TrackSend(req)
	resp, err := http.DefaultClient.Do(tracked)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !wrote {
		t.Error("the trace already on the request was dropped")
	}
	if !sent() {
		t.Error("the request counted as not sent")
	}
}

func TestTrackSendLeavesTheRequestAlone(t *testing.T) {
	req := post(t, "http://127.0.0.1:1/token")
	tracked, sent := TrackSend(req)
	if tracked == req {
		t.Error("TrackSend returned the request it was given")
	}
	if httptrace.ContextClientTrace(req.Context()) != nil {
		t.Error("TrackSend put a trace on the original request")
	}
	if sent() {
		t.Error("a request that was never sent counted as sent")
	}
}

func TestTrackSendContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ctx, sent := TrackSendContext(context.Background())
	if sent() {
		t.Error("nothing was sent yet")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !sent() {
		t.Error("a request made with the context counted as not sent")
	}
}

// A server may close a kept-alive connection while it is idle. A request the client writes to it
// before it sees the close fails after it was written, so it counts as sent, though the server
// never read it. A tracked request through Upstream goes out on a new connection instead: a
// server that still listens answers it, and one that is gone refuses the connection, which counts
// as not sent.
func TestTrackSendStaleConnection(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name := "HTTP/1.1"
		if h2 {
			name = "HTTP/2"
		}
		t.Run(name, func(t *testing.T) {
			// The control: the client does keep the connection and writes the next request to it.
			t.Run("an untracked request is written to the closed connection", func(t *testing.T) {
				srv, client, stale := staleSetup(t, h2)
				stale(srv.CloseClientConnections)
				resp, err := client.Do(post(t, srv.URL+"/token"))
				if err == nil {
					resp.Body.Close()
					t.Fatal("a request got an answer on the connection the server closed: the connection was not stale")
				}
			})
			t.Run("the server still listens", func(t *testing.T) {
				srv, client, stale := staleSetup(t, h2)
				stale(srv.CloseClientConnections)
				tracked, sent := TrackSend(post(t, srv.URL+"/token"))
				resp, err := client.Do(tracked)
				if err != nil {
					t.Fatalf("a tracked request failed on a server that still listens: %v (counted as sent: %v)", err, sent())
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("status = %d, want 200", resp.StatusCode)
				}
			})
			t.Run("the server is gone", func(t *testing.T) {
				srv, client, stale := staleSetup(t, h2)
				stale(srv.Close)
				err, sent := trackedDo(t, client, post(t, srv.URL+"/token"))
				if err == nil {
					t.Fatal("a server that was shut down answered")
				}
				if sent {
					t.Errorf("a request to a server that was shut down counted as sent: %v", err)
				}
			})
		})
	}
}

// A tracked request of a call whose account has a proxy goes through that proxy, on a new
// connection each time; untracked requests through it share one.
func TestTrackSendThroughProxy(t *testing.T) {
	var mu sync.Mutex
	var got []string // each request's connection and URL, as the proxy saw them
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, r.RemoteAddr+" "+r.URL.String())
		mu.Unlock()
	}))
	defer proxy.Close()
	client := &http.Client{Transport: Upstream(&http.Transport{}), Timeout: 10 * time.Second}
	ctx, _ := withUpstreamInfo(context.Background(), protocol.Call{Type: protocol.TypeCall, ID: "r", Op: protocol.OpRefresh,
		Account: &protocol.Account{ID: "a", ProxyURL: proxy.URL}})
	const target = "http://login.pluxy-test.invalid/token"

	send := func(track bool) string {
		t.Helper()
		req := post(t, target).WithContext(ctx)
		if track {
			req, _ = TrackSend(req)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			t.Fatal("the request did not go through the account's proxy")
		}
		conn, url, _ := strings.Cut(got[len(got)-1], " ")
		if url != target {
			t.Fatalf("the proxy got %q, want %q", url, target)
		}
		got = got[:0]
		return conn
	}
	if a, b := send(false), send(false); a != b {
		t.Errorf("untracked requests came on connections %s and %s, want one shared", a, b)
	}
	if a, b := send(true), send(true); a == b {
		t.Errorf("tracked requests shared connection %s, want a new one each", a)
	}
}

// staleSetup starts a server, a client wrapped in Upstream, and sends one untracked request, which
// leaves an idle kept-alive connection in the client's pool. stale(close) makes that connection
// stale: close is the server closing it, and the client sees the close only once it writes to the
// connection again, so its next request on it is written to a connection the server has closed.
func staleSetup(t *testing.T, h2 bool) (*httptest.Server, *http.Client, func(close func())) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	base := &http.Transport{}
	if h2 {
		srv.EnableHTTP2 = true
		srv.StartTLS()
		base = srv.Client().Transport.(*http.Transport).Clone()
		base.ForceAttemptHTTP2 = true
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	d := &staleDialer{}
	t.Cleanup(d.release)
	base.DialContext = d.dial
	client := &http.Client{Transport: Upstream(base), Timeout: 10 * time.Second}

	resp, err := client.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	// Reading the body to its end returns the connection to the pool before Read reports the end.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if h2 != (resp.ProtoMajor == 2) {
		t.Fatalf("protocol = %s, want HTTP/2: %v", resp.Proto, h2)
	}
	return srv, client, func(close func()) {
		if n := d.hold(); n != 1 {
			t.Fatalf("the client has %d connections, want 1", n)
		}
		close()
	}
}

// staleDialer dials connections that can be made stale: once held, a connection's reads, among
// them its read of the server's close, wait until the client writes to it again.
type staleDialer struct {
	mu    sync.Mutex
	conns []*staleConn
}

func (d *staleDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	s := &staleConn{Conn: c, wrote: make(chan struct{})}
	d.mu.Lock()
	d.conns = append(d.conns, s)
	d.mu.Unlock()
	return s, nil
}

// hold holds every connection dialed so far and returns how many there are.
func (d *staleDialer) hold() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		c.held.Store(true)
	}
	return len(d.conns)
}

// release lets every held read finish.
func (d *staleDialer) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		c.once.Do(func() { close(c.wrote) })
	}
}

type staleConn struct {
	net.Conn
	held  atomic.Bool
	once  sync.Once
	wrote chan struct{} // closed at the first write after the connection was held
}

// Read returns what it read only after the next write once the connection is held: over TLS the
// server's close arrives as a close_notify alert, which is data, not an error.
func (c *staleConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if c.held.Load() {
		<-c.wrote
	}
	return n, err
}

func (c *staleConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if c.held.Load() {
		c.once.Do(func() { close(c.wrote) })
	}
	return n, err
}
