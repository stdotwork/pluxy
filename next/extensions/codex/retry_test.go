package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

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
		{"connection refused", sdktest.ClosedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", sdktest.LoginServer(t, sdktest.HangUp), llm.CodeUnavailable, false},
		{"503", sdktest.LoginServer(t, status(503)), llm.CodeUnavailable, false},
		{"429", sdktest.LoginServer(t, status(429)), llm.CodeRateLimit, true},
		{"reply broke off", sdktest.LoginServer(t, brokenReply), llm.CodeUnavailable, false},
		{"reply has no token", sdktest.LoginServer(t, func(w http.ResponseWriter) { io.WriteString(w, "{}") }), llm.CodeUpstream, false},
	} {
		settings, _ := json.Marshal(map[string]any{"auth_base_url": c.url, "callback_listen": false})
		t.Run("renewal "+c.name, func(t *testing.T) {
			_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: settings,
				Credential: subscription("a", "r")}})
			sdktest.WantError(t, err, c.code, c.retryable)
		})
		t.Run("device start "+c.name, func(t *testing.T) {
			set, _ := readSettings(settings)
			// Every case ends at the first request, before the user is prompted (the session is nil).
			_, err := deviceLogin(context.Background(), set, nil)
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}
