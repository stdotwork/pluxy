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
	"time"

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

// closedURL returns the address of a port nothing listens on: port 1 on loopback, which no test
// server can be given, unlike a just-closed ephemeral port that the next httptest server may reuse.
func closedURL(t *testing.T) string {
	t.Helper()
	const addr = "127.0.0.1:1"
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatalf("something listens on %s; the connection-refused cases need it closed", addr)
	}
	return "http://" + addr
}

// hangUp closes the connection of the request being answered without a reply.
func hangUp(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		c.Close()
	}
}

// settingsFor returns the account settings that send login calls to base.
func settingsFor(base string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"auth_base_url": base})
	return b
}

// A login or renewal call is retryable only when the request never reached Kimi.
func TestLoginRetryOnlyWhenNotSent(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	ctx := context.Background()
	hungUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hangUp(w) }))
	defer hungUp.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"access_token":`)
		hangUp(w)
	}))
	defer broken.Close()
	closed := closedURL(t)
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
			wantError(t, err, c.code, c.retryable)
		})
		t.Run("device start "+c.name, func(t *testing.T) {
			_, err := deviceLogin(ctx, settingsOf(t, settingsFor(c.base)), noPrompt{})
			wantError(t, err, c.code, c.retryable)
		})
	}
}
