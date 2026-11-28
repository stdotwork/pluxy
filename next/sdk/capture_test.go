package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

type gatedBody struct {
	r     io.Reader
	gate  <-chan struct{}
	read  *atomic.Int64
	early *atomic.Int64
}

func (g *gatedBody) Read(p []byte) (int, error) {
	if g.gate != nil {
		select {
		case <-g.gate:
		case <-time.After(2 * time.Second):
			g.early.Add(1)
			return 0, errors.New("body read before the upstream got the request")
		}
	}
	n, err := g.r.Read(p)
	g.read.Add(int64(n))
	return n, err
}

func (g *gatedBody) Close() error { return nil }

func echoUpstream(t *testing.T, got chan struct{}) *httptest.Server {
	t.Helper()
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(got) })
		_, _ = io.Copy(w, r.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newBodyRequest(t *testing.T, url, body string, read, early *atomic.Int64, gate <-chan struct{}) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	req.Body = &gatedBody{r: strings.NewReader(body), gate: gate, read: read, early: early}
	req.GetBody = func() (io.ReadCloser, error) {
		return &gatedBody{r: strings.NewReader(body), gate: gate, read: read, early: early}, nil
	}
	return req
}

func roundTripCaptured(t *testing.T, c *captureLog, req *http.Request) string {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	resp, err := c.roundTrip(tr, req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	return string(reply)
}

func TestCaptureDoesNotReadBodyBeforeSending(t *testing.T) {
	const payload = `{"model":"example-model","input":"hello"}`
	got := make(chan struct{})
	srv := echoUpstream(t, got)
	var read, early atomic.Int64
	c := &captureLog{}
	reply := roundTripCaptured(t, c, newBodyRequest(t, srv.URL, payload, &read, &early, got))
	if n := early.Load(); n != 0 {
		t.Errorf("capture read the body %d time(s) before the upstream had the request", n)
	}
	if reply != payload {
		t.Errorf("upstream got %q, want %q", reply, payload)
	}
	d := c.take()
	if d == nil || len(d.Upstream) != 1 || d.Upstream[0].Request.Body != payload || d.Upstream[0].Request.BodySize != int64(len(payload)) {
		t.Errorf("captured request body: %+v, want %q", d, payload)
	}
}

func TestCaptureReadsBodyOnce(t *testing.T) {
	const payload = `{"model":"example-model","input":"hello"}`
	got := make(chan struct{})
	srv := echoUpstream(t, got)
	var read, early atomic.Int64
	c := &captureLog{}
	roundTripCaptured(t, c, newBodyRequest(t, srv.URL, payload, &read, &early, nil))
	if n := read.Load(); n != int64(len(payload)) {
		t.Errorf("%d bytes were read from the body and its copies, want %d (once)", n, len(payload))
	}
}

func TestCaptureBodyWhenGetBodyFails(t *testing.T) {
	const payload = `{"model":"example-model","input":"hello"}`
	got := make(chan struct{})
	srv := echoUpstream(t, got)
	var read, early atomic.Int64
	req := newBodyRequest(t, srv.URL, payload, &read, &early, nil)
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("no replay copy") }
	c := &captureLog{}
	if reply := roundTripCaptured(t, c, req); reply != payload {
		t.Fatalf("upstream got %q, want %q", reply, payload)
	}
	d := c.take()
	if d == nil || len(d.Upstream) != 1 || d.Upstream[0].Request.Body != payload {
		t.Errorf("captured request body: %+v, want %q", d, payload)
	}
}

func TestCaptureReplayedBody(t *testing.T) {
	const payload = `{"model":"example-model","input":"hello"}`
	var read, early atomic.Int64
	req := newBodyRequest(t, "http://upstream.invalid/v1", payload, &read, &early, nil)
	replay := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.ReadFull(r.Body, make([]byte, 10))
		b, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(io.Discard, b); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	c := &captureLog{}
	resp, err := c.roundTrip(replay, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	d := c.take()
	if d == nil || len(d.Upstream) != 1 || d.Upstream[0].Request.Body != payload || d.Upstream[0].Request.BodySize != int64(len(payload)) {
		t.Errorf("captured request body: %+v, want %q once", d, payload)
	}
}

func TestCaptureUnusedReplay(t *testing.T) {
	const payload = `{"model":"example-model","input":"hello"}`
	var read, early atomic.Int64
	req := newBodyRequest(t, "http://upstream.invalid/v1", payload, &read, &early, nil)
	unused := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		b, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		b.Close()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	c := &captureLog{}
	resp, err := c.roundTrip(unused, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	d := c.take()
	if d == nil || len(d.Upstream) != 1 || d.Upstream[0].Request.Body != payload || d.Upstream[0].Request.BodySize != int64(len(payload)) {
		t.Errorf("captured request body: %+v, want %q", d, payload)
	}
}

func TestCaptureInterleavedReplay(t *testing.T) {
	const payload = `{"model":"example-model","input":"hello"}`
	var read, early atomic.Int64
	req := newBodyRequest(t, "http://upstream.invalid/v1", payload, &read, &early, nil)
	interleaved := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		one := make([]byte, 1)
		if _, err := io.ReadFull(r.Body, one); err != nil {
			return nil, err
		}
		b, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(b, make([]byte, 2)); err != nil {
			return nil, err
		}
		b.Close()
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	c := &captureLog{}
	resp, err := c.roundTrip(interleaved, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	d := c.take()
	if d == nil || len(d.Upstream) != 1 || d.Upstream[0].Request.Body != payload || d.Upstream[0].Request.BodySize != int64(len(payload)) {
		t.Errorf("captured request body: %+v, want %q", d, payload)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCaptureTakeDuringRoundTrip(t *testing.T) {
	c := &captureLog{}
	release := make(chan struct{})
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		<-release
		_, _ = io.Copy(io.Discard, r.Body)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	req, err := http.NewRequest(http.MethodPost, "http://upstream.invalid/v1", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = nil
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := c.roundTrip(rt, req)
		if err != nil {
			t.Error(err)
			return
		}
		resp.Body.Close()
	}()
	for {
		c.mu.Lock()
		recorded := c.first != nil
		c.mu.Unlock()
		if recorded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	d := c.take()
	close(release)
	<-done
	if d == nil || len(d.Upstream) != 1 {
		t.Fatalf("diagnostic: %+v, want one exchange", d)
	}
}

func TestCapturedURL(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      string
		notWant   []string
	}{
		{name: "plain", raw: "https://api.example.test/v1/chat?model=m&stream=true", want: "https://api.example.test/v1/chat?model=m&stream=true"},
		{name: "user info", raw: "https://user:pass@api.example.test/v1", want: "https://api.example.test/v1"},
		{name: "secret value", raw: "https://api.example.test/v1?model=m&api_key=sk-fake-1234", want: "https://api.example.test/v1?api_key=%5Bredacted%5D&model=m"},
		{name: "secret value with a bad escape", raw: "https://api.example.test/v1?api_key=private%zz", notWant: []string{"private", "%zz"}},
		{name: "secret value beside a bad escape", raw: "https://api.example.test/v1?model=m%zz&token=private", notWant: []string{"private"}},
		{name: "secret key with a bad escape", raw: "https://api.example.test/v1?api_key%zz=private", notWant: []string{"private"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			got := capturedURL(u)
			if tc.want != "" && got != tc.want {
				t.Errorf("capturedURL = %q, want %q", got, tc.want)
			}
			for _, s := range tc.notWant {
				if strings.Contains(got, s) {
					t.Errorf("capturedURL = %q, which holds %q", got, s)
				}
			}
		})
	}
}

func headersLeftOut(t *testing.T, r protocol.UpstreamRequest) int {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	n, _ := m["headers_left_out"].(float64)
	return int(n)
}

func TestCaptureBoundsHeaders(t *testing.T) {
	bound := 16 << 10
	capture := func(t *testing.T, h http.Header) protocol.UpstreamRequest {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/v1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = h
		c := &captureLog{}
		resp, err := c.roundTrip(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		}), req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		d := c.take()
		if d == nil || len(d.Upstream) != 1 {
			t.Fatalf("diagnostic: %+v", d)
		}
		return d.Upstream[0].Request
	}
	size := func(h map[string][]string) (lines, bytes int) {
		for k, vs := range h {
			for _, v := range vs {
				lines++
				bytes += len(k) + len(v) + 3
			}
		}
		return
	}
	t.Run("large values", func(t *testing.T) {
		h := http.Header{}
		for _, k := range []string{"X-A", "X-B", "X-C"} {
			h.Set(k, strings.Repeat("v", 6000))
		}
		r := capture(t, h)
		if _, n := size(r.Headers); n > bound {
			t.Errorf("captured %d header bytes, bound %d", n, bound)
		}
		if lines, _ := size(r.Headers); lines != 2 {
			t.Errorf("kept %d header lines, want 2 (the first two by name)", lines)
		}
		want := strings.Repeat("v", 6000)
		for _, k := range []string{"X-A", "X-B"} {
			if vs := r.Headers[k]; len(vs) != 1 || vs[0] != want {
				t.Errorf("header %s kept as %d values (%d bytes in the first), want its one value whole", k, len(vs), len(strings.Join(vs[:min(1, len(vs))], "")))
			}
		}
		if n := headersLeftOut(t, r); n != 1 {
			t.Errorf("headers_left_out = %d, want 1", n)
		}
	})
	t.Run("many lines", func(t *testing.T) {
		h := http.Header{}
		for i := range 10000 {
			h[fmt.Sprintf("X-H%05d", i)] = []string{"v"}
		}
		r := capture(t, h)
		lines, n := size(r.Headers)
		if n > bound {
			t.Errorf("captured %d header bytes, bound %d", n, bound)
		}
		if got := headersLeftOut(t, r); got != 10000-lines || got == 0 {
			t.Errorf("headers_left_out = %d with %d lines kept of 10000", got, lines)
		}
	})
	t.Run("within the bound", func(t *testing.T) {
		r := capture(t, http.Header{"X-A": {"1", "2"}, "X-B": {"3"}})
		if lines, _ := size(r.Headers); lines != 3 || headersLeftOut(t, r) != 0 {
			t.Errorf("headers %v, left out %d: want all 3 lines", r.Headers, headersLeftOut(t, r))
		}
	})
}

func TestCaptureMarksStoppedRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/empty" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, strings.Repeat("0123456789", 100))
	}))
	t.Cleanup(srv.Close)
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	for _, tc := range []struct {
		name, path string
		read       func(io.Reader)
		stopped    bool
	}{
		{"read to its end", "/", func(r io.Reader) { _, _ = io.ReadAll(r) }, false},
		{"limited read", "/", func(r io.Reader) { _, _ = io.ReadAll(io.LimitReader(r, 10)) }, true},
		{"closed part way", "/", func(r io.Reader) { _, _ = io.ReadFull(r, make([]byte, 5)) }, true},
		{"empty, never read", "/empty", func(io.Reader) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+tc.path, strings.NewReader(`{"sent":"whole"}`))
			if err != nil {
				t.Fatal(err)
			}
			c := &captureLog{}
			resp, err := c.roundTrip(tr, req)
			if err != nil {
				t.Fatal(err)
			}
			tc.read(resp.Body)
			resp.Body.Close()
			d := c.take()
			if d == nil || len(d.Upstream) != 1 || d.Upstream[0].Response == nil {
				t.Fatalf("captured %+v, want one exchange with its response", d)
			}
			x := d.Upstream[0]
			if x.Request.Body != `{"sent":"whole"}` || x.Request.BodyStopped {
				t.Errorf("request body %q, stopped %v: want it whole and not stopped", x.Request.Body, x.Request.BodyStopped)
			}
			if x.Response.BodyStopped != tc.stopped {
				t.Errorf("response body of %d bytes: stopped %v, want %v", x.Response.BodySize, x.Response.BodyStopped, tc.stopped)
			}
		})
	}
}

type lateEOF struct {
	r       *strings.Reader
	release <-chan struct{}
}

func (l *lateEOF) Read(p []byte) (int, error) {
	if l.r.Len() == 0 {
		<-l.release
	}
	return l.r.Read(p)
}

func (l *lateEOF) Close() error { return nil }

func TestCaptureRequestBodySentWhole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	release := make(chan struct{})
	defer close(release)
	const body = `{"sent":"whole"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Body, req.ContentLength = &lateEOF{r: strings.NewReader(body), release: release}, int64(len(body))
	c := &captureLog{}
	resp, err := c.roundTrip(tr, req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	d := c.take()
	if d == nil || len(d.Upstream) != 1 {
		t.Fatalf("captured %+v, want one exchange", d)
	}
	if x := d.Upstream[0].Request; x.Body != body || x.BodyStopped {
		t.Errorf("request body %q, stopped %v: want it whole and not stopped", x.Body, x.BodyStopped)
	}
}
