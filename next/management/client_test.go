package management

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestGettersReturnTheDecodedReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case Prefix + "/status":
			w.Write([]byte(`{"version":"1.2.3","active":{"revision":7},"extensions":[{"id":"pluxy/claude","state":"running"}]}`))
		case Prefix + "/accounts":
			w.Write([]byte(`{"accounts":[{"id":"a1"},{"id":"a2"}]}`))
		case Prefix + "/client-keys":
			w.Write([]byte(`{"keys":[{"id":"k1","masked":"sk**1"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL}
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil || st.Version != "1.2.3" || st.Active.Revision != 7 || len(st.Extensions) != 1 || st.Extensions[0].State != "running" {
		t.Errorf("Status = %+v, %v; want the reply's version, revision and extension", st, err)
	}
	accounts, err := c.Accounts(ctx)
	if err != nil || len(accounts) != 2 || accounts[1].ID != "a2" {
		t.Errorf("Accounts = %+v, %v; want both accounts", accounts, err)
	}
	keys, err := c.ClientKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].Masked != "sk**1" {
		t.Errorf("ClientKeys = %+v, %v; want the reply's key", keys, err)
	}
}

type repeatReader struct {
	n int64
	b byte
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.n {
		n = r.n
	}
	for i := range p[:n] {
		p[i] = r.b
	}
	r.n -= n
	return int(n), nil
}

func TestStreamCopiesAReplyAboveTheCallLimit(t *testing.T) {
	const size = maxReply + 5<<20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Prefix+"/big" || r.URL.Query().Get("x") != "1" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(size))
		io.Copy(w, &repeatReader{n: size, b: 'a'})
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Key: "k"}
	var n countWriter
	if err := c.Stream(context.Background(), http.MethodGet, "/big", url.Values{"x": {"1"}}, &n); err != nil || n.n != size {
		t.Fatalf("Stream wrote %d bytes, %v; want all %d", n.n, err, size)
	}
}

type countWriter struct{ n int64 }

func (w *countWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return len(p), nil }

func TestStreamErrorsAreCallErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == Prefix+"/plain" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":"not_found","message":"no log file of that name"}}`))
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL}
	var out bytes.Buffer
	err := c.Stream(context.Background(), http.MethodGet, "/x", nil, &out)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Code != "not_found" || ae.Message != "no log file of that name" || out.Len() != 0 {
		t.Errorf("Stream of a 404 = %v, %d bytes written; want the API error and nothing written", err, out.Len())
	}
	err = c.Stream(context.Background(), http.MethodGet, "/plain", nil, &out)
	if !errors.As(err, &ae) || ae.Status != 502 || ae.Code != "" || out.Len() != 0 {
		t.Errorf("Stream of a bare 502 = %v; want an API error with the status in its message", err)
	}
	if err := (&Client{URL: "http://127.0.0.1:1"}).Stream(context.Background(), http.MethodGet, "/x", nil, &out); !IsUnreachable(err) {
		t.Errorf("Stream with no core = %v, want an unreachable error", err)
	}
}

func TestStreamBrokenMidway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := (&Client{URL: srv.URL}).Stream(context.Background(), http.MethodGet, "/x", nil, &out)
	if err == nil || out.String() != "partial" {
		t.Errorf("Stream of a broken reply = %v, wrote %q; want an error after the bytes that arrived", err, out.String())
	}
}

func TestStreamWriterError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer srv.Close()
	want := errors.New("pipe closed")
	err := (&Client{URL: srv.URL}).Stream(context.Background(), http.MethodGet, "/x", nil, failWriter{want})
	if !errors.Is(err, want) || IsUnreachable(err) {
		t.Errorf("Stream into a failing writer = %v, want that writer's error", err)
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }
