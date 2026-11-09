package sdk

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
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
