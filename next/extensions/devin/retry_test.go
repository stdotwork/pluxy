package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func wantError(t *testing.T, err error, code string, retryable bool) {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v, want an *llm.Error", err)
	}
	if e.Code != code || e.Retryable != retryable {
		t.Errorf("got code %s retryable %v, want code %s retryable %v (%s)", e.Code, e.Retryable, code, retryable, e.Message)
	}
}

// closedURL returns the address of a port nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

// hangUp closes the connection of the request being answered without a reply.
func hangUp(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		c.Close()
	}
}

// loginServer is a server whose every reply is answer.
func loginServer(t *testing.T, answer func(w http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { answer(w) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func status(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { http.Error(w, "down", code) }
}

// Trading the sign-in code is retryable only when the request never reached Devin, or when Devin
// rate limited it.
func TestExchangeRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", closedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", loginServer(t, hangUp), llm.CodeUnavailable, false},
		{"503", loginServer(t, status(503)), llm.CodeUnavailable, false},
		{"429", loginServer(t, status(429)), llm.CodeRateLimit, true},
		{"reply broke off", loginServer(t, func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"token":`)
			hangUp(w)
		}), llm.CodeUnavailable, false},
		{"reply has no token", loginServer(t, func(w http.ResponseWriter) { io.WriteString(w, "{}") }), llm.CodeUpstream, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := exchange(context.Background(), settings{APIBaseURL: c.url}, "code", "verifier")
			wantError(t, err, c.code, c.retryable)
		})
	}
}
