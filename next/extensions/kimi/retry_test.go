package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

// settingsFor returns the account settings that send login calls to base.
func settingsFor(base string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"auth_base_url": base})
	return b
}

// A login or renewal call is retryable only when the request never reached Kimi.
func TestLoginRetryOnlyWhenNotSent(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	ctx := context.Background()
	hungUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sdktest.HangUp(w) }))
	defer hungUp.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"access_token":`)
		sdktest.HangUp(w)
	}))
	defer broken.Close()
	closed := sdktest.ClosedURL(t)
	for _, c := range []struct {
		name      string
		base      string
		retryable bool
		code      string
	}{
		{"connection refused", closed, true, llm.CodeUnavailable},
		{"hung up after the request", hungUp.URL, false, llm.CodeUnavailable},
		{"reply broke off", broken.URL, false, llm.CodeUpstream},
	} {
		t.Run("renewal "+c.name, func(t *testing.T) {
			acct := protocol.Account{Settings: settingsFor(c.base),
				Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}
			_, err := refresh(ctx, sdk.RefreshCall{Account: acct})
			sdktest.WantError(t, err, c.code, c.retryable)
		})
		t.Run("device start "+c.name, func(t *testing.T) {
			_, err := deviceLogin(ctx, settingsOf(t, settingsFor(c.base)), noPrompt{})
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}
