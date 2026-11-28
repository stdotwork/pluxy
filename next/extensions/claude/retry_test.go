package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func status(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { http.Error(w, `{"error":"down"}`, code) }
}

func brokenReply(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "100")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, `{"access_token":`)
	sdktest.HangUp(w)
}

func TestRefreshRetryOnlyWhenNotSent(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
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
		{"reply broke off", sdktest.LoginServer(t, brokenReply), llm.CodeUpstream, false},
		{"reply is not JSON", sdktest.LoginServer(t, func(w http.ResponseWriter) { io.WriteString(w, "not json") }), llm.CodeUpstream, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			settings, _ := json.Marshal(map[string]string{"token_url": c.url + "/v1/oauth/token"})
			_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: settings,
				Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}})
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}

func TestRefreshOutage(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	srv := claudetest.New(t)
	srv.TokenDown = true
	access, refreshToken := srv.Grant()
	_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(""),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refreshToken}}})
	sdktest.WantError(t, err, llm.CodeUnavailable, false)
}

func TestAPIKeyLoginRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", sdktest.ClosedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", sdktest.LoginServer(t, sdktest.HangUp), llm.CodeUnavailable, false},
		{"500", sdktest.LoginServer(t, status(500)), llm.CodeUpstream, false},
		{"503", sdktest.LoginServer(t, status(503)), llm.CodeUnavailable, false},
		{"529", sdktest.LoginServer(t, status(529)), llm.CodeUnavailable, false},
		{"429", sdktest.LoginServer(t, status(429)), llm.CodeRateLimit, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := apiKeyLogin(context.Background(), settings{BaseURL: c.url}, "sk-ant-api-key")
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}

func TestModelRequestOutageStaysRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusServiceUnavailable)
	if e := upstreamError(rec.Result(), "m", false); e.Code != llm.CodeUnavailable || !e.Retryable {
		t.Errorf("503 on a model request: %+v, want unavailable and retryable", e)
	}
}
