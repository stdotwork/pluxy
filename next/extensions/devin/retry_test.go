package main

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func status(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { http.Error(w, "down", code) }
}

func TestExchangeRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", sdktest.ClosedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", sdktest.LoginServer(t, sdktest.HangUp), llm.CodeUnavailable, false},
		{"503", sdktest.LoginServer(t, status(503)), llm.CodeUnavailable, false},
		{"429", sdktest.LoginServer(t, status(429)), llm.CodeRateLimit, true},
		{"reply broke off", sdktest.LoginServer(t, func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"token":`)
			sdktest.HangUp(w)
		}), llm.CodeUnavailable, false},
		{"reply has no token", sdktest.LoginServer(t, func(w http.ResponseWriter) { io.WriteString(w, "{}") }), llm.CodeUpstream, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := exchange(context.Background(), settings{APIBaseURL: c.url}, "code", "verifier")
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}
