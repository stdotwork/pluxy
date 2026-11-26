package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

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

// hangUpURL returns the address of a server that reads each request and closes the connection
// without a reply.
func hangUpURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			var got string
			for !strings.Contains(got, "\r\n\r\n") {
				n, err := c.Read(buf)
				if err != nil {
					break
				}
				got += string(buf[:n])
			}
			c.Close()
		}
	}()
	return "http://" + l.Addr().String()
}

func wantLLMError(t *testing.T, err error, code string, retryable bool) {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v, want an *llm.Error", err)
	}
	if e.Code != code || e.Retryable != retryable {
		t.Errorf("got code %s retryable %v, want code %s retryable %v (%s)", e.Code, e.Retryable, code, retryable, e.Message)
	}
}

func oauthAccount(base string) protocol.Account {
	return account(base, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "old", RefreshToken: "refresh-token"})
}

func TestRefreshRetryOnlyWhenNotSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Run("connection refused is retryable", func(t *testing.T) {
		_, err := refresh(ctx, sdk.RefreshCall{Account: oauthAccount(closedURL(t))})
		wantLLMError(t, err, llm.CodeUnavailable, true)
	})
	t.Run("hung up after the request was read is not", func(t *testing.T) {
		_, err := refresh(ctx, sdk.RefreshCall{Account: oauthAccount(hangUpURL(t))})
		wantLLMError(t, err, llm.CodeUnavailable, false)
	})
	t.Run("503 is not", func(t *testing.T) {
		srv := oauthtest.New(t)
		srv.RefreshFailure = "unavailable"
		_, err := refresh(ctx, sdk.RefreshCall{Account: oauthAccount(srv.URL)})
		wantLLMError(t, err, llm.CodeUnavailable, false)
	})
}

func TestDeviceLoginRetryOnlyWhenNotSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := deviceLogin(ctx, closedURL(t), nil)
	wantLLMError(t, err, llm.CodeUnavailable, true)
	_, err = deviceLogin(ctx, hangUpURL(t), nil)
	wantLLMError(t, err, llm.CodeUnavailable, false)
}

func TestAPIKeyLoginRetryOnlyWhenNotSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	in := map[string]string{"api_key": "key"}
	_, err := apiKeyLogin(ctx, closedURL(t), in)
	wantLLMError(t, err, llm.CodeUnavailable, true)
	_, err = apiKeyLogin(ctx, hangUpURL(t), in)
	wantLLMError(t, err, llm.CodeUnavailable, false)
}

// A generate call that got no reply stays retryable: the model request certainly did not run
// to the end, and the retry rule for login calls does not apply to it.
func TestChatErrorStaysRetryable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := chat(ctx, hangUpURL(t), "token", "model", false, false)
	wantLLMError(t, err, llm.CodeUnavailable, true)
}
