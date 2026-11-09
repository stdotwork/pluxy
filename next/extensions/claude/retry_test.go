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

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
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

// A renewal is retryable only when the request never reached the token endpoint, or when the
// endpoint rate limited it.
func TestRefreshRetryOnlyWhenNotSent(t *testing.T) {
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
		{"reply broke off", loginServer(t, brokenReply), llm.CodeUpstream, false},
		{"reply is not JSON", loginServer(t, func(w http.ResponseWriter) { io.WriteString(w, "not json") }), llm.CodeUpstream, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			settings, _ := json.Marshal(map[string]string{"token_url": c.url + "/v1/oauth/token"})
			_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: settings,
				Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}})
			wantError(t, err, c.code, c.retryable)
		})
	}
}

// The same rule holds for the token endpoint of the claudetest server.
func TestRefreshOutage(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	srv := claudetest.New(t)
	srv.TokenDown = true
	access, refreshToken := srv.Grant()
	_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(""),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refreshToken}}})
	wantError(t, err, llm.CodeUnavailable, false)
}

// An API key login is checked by one request: the same rule applies to it, and a 5xx is not
// retryable even where the same status on a model request is.
func TestAPIKeyLoginRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", closedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", loginServer(t, hangUp), llm.CodeUnavailable, false},
		{"500", loginServer(t, status(500)), llm.CodeUpstream, false},
		{"503", loginServer(t, status(503)), llm.CodeUnavailable, false},
		{"529", loginServer(t, status(529)), llm.CodeUnavailable, false},
		{"429", loginServer(t, status(429)), llm.CodeRateLimit, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := apiKeyLogin(context.Background(), settings{BaseURL: c.url}, "sk-ant-api-key")
			wantError(t, err, c.code, c.retryable)
		})
	}
}

// A model request that Anthropic refuses with a 503 stays retryable: it did not run.
func TestModelRequestOutageStaysRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusServiceUnavailable)
	if e := upstreamError(rec.Result(), "m"); e.Code != llm.CodeUnavailable || !e.Retryable {
		t.Errorf("503 on a model request: %+v, want unavailable and retryable", e)
	}
}
