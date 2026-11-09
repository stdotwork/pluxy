package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
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
	return func(w http.ResponseWriter) { http.Error(w, `{"error":"down"}`, code) }
}

func brokenReply(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "100")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, `{"access_token":`)
	hangUp(w)
}

// A login or renewal call is retryable only when the request never reached the login server, or
// when the server rate limited it.
func TestLoginRetryOnlyWhenNotSent(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
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
		{"reply broke off", loginServer(t, brokenReply), llm.CodeUnavailable, false},
		{"reply has no token", loginServer(t, func(w http.ResponseWriter) { io.WriteString(w, "{}") }), llm.CodeUpstream, false},
	} {
		settings, _ := json.Marshal(map[string]any{"auth_base_url": c.url, "callback_listen": false})
		t.Run("renewal "+c.name, func(t *testing.T) {
			_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: settings,
				Credential: subscription("a", "r")}})
			wantError(t, err, c.code, c.retryable)
		})
		t.Run("device start "+c.name, func(t *testing.T) {
			set, _ := readSettings(settings)
			// Every case ends at the first request, before the user is prompted (the session is nil).
			_, err := deviceLogin(context.Background(), set, nil)
			wantError(t, err, c.code, c.retryable)
		})
	}
}
