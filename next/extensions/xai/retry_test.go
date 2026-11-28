package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/xai/xaitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func refreshCall(srv *xaitest.Server, tokenEndpoint string) sdk.RefreshCall {
	access, rt := srv.Grant()
	data, _ := json.Marshal(credentialData{TokenEndpoint: tokenEndpoint})
	return sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}}
}

func TestLoginRetryOnlyWhenNotSent(t *testing.T) {
	ctx := context.Background()
	t.Run("discovery refused", func(t *testing.T) {
		srv := xaitest.New(t)
		set, _ := readSettings(srv.Settings())
		set.AuthIssuer = sdktest.ClosedURL(t)
		_, err := deviceLogin(ctx, set, promptFunc(func(protocol.Prompt) {}))
		sdktest.WantError(t, err, llm.CodeUnavailable, true)
	})
	t.Run("device start hung up", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/.well-known/openid-configuration" {
				return false
			}
			sdktest.HangUp(w)
			return true
		}
		set, _ := readSettings(srv.Settings())
		_, err := deviceLogin(ctx, set, promptFunc(func(protocol.Prompt) {}))
		sdktest.WantError(t, err, llm.CodeUnavailable, false)
	})
	t.Run("renewal refused", func(t *testing.T) {
		srv := xaitest.New(t)
		closed := sdktest.ClosedURL(t)
		call := refreshCall(srv, closed+"/oauth2/token")
		call.Account.Settings, _ = json.Marshal(map[string]string{"auth_issuer": closed})
		_, err := refresh(ctx, call)
		sdktest.WantError(t, err, llm.CodeUnavailable, true)
	})
	t.Run("renewal hung up", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/oauth2/token" {
				return false
			}
			sdktest.HangUp(w)
			return true
		}
		_, err := refresh(ctx, refreshCall(srv, srv.URL+"/oauth2/token"))
		sdktest.WantError(t, err, llm.CodeUnavailable, false)
	})
	t.Run("renewal 503", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.TokenDown = true
		_, err := refresh(ctx, refreshCall(srv, srv.URL+"/oauth2/token"))
		sdktest.WantError(t, err, llm.CodeUnavailable, false)
	})
	t.Run("renewal reply broke off", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/oauth2/token" {
				return false
			}
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"access_token":`)
			sdktest.HangUp(w)
			return true
		}
		_, err := refresh(ctx, refreshCall(srv, srv.URL+"/oauth2/token"))
		sdktest.WantError(t, err, llm.CodeUnavailable, false)
	})
	t.Run("renewal 429", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/oauth2/token" {
				return false
			}
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return true
		}
		_, err := refresh(ctx, refreshCall(srv, srv.URL+"/oauth2/token"))
		sdktest.WantError(t, err, llm.CodeRateLimit, true)
	})
}

func stubWait(t *testing.T, cancel context.CancelFunc, limit int32) {
	t.Helper()
	old := waitPoll
	t.Cleanup(func() { waitPoll = old })
	var waits atomic.Int32
	waitPoll = func(ctx context.Context, _ time.Duration) bool {
		if waits.Add(1) >= limit {
			cancel()
		}
		return ctx.Err() == nil
	}
}

func TestDevicePollStopsWhenItMayHaveRun(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func(w http.ResponseWriter)
	}{
		{"503", func(w http.ResponseWriter) { http.Error(w, "upstream connect error", http.StatusServiceUnavailable) }},
		{"503 saying pending", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"authorization_pending"}`)
		}},
		{"hung up", sdktest.HangUp},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := xaitest.New(t)
			var polls atomic.Int32
			srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/oauth2/token" {
					return false
				}
				polls.Add(1)
				c.answer(w)
				return true
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stubWait(t, cancel, 3)
			set, _ := readSettings(srv.Settings())
			_, err := deviceLogin(ctx, set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
			sdktest.WantError(t, err, llm.CodeUnavailable, false)
			if n := polls.Load(); n != 1 {
				t.Errorf("%d polls reached xAI, want 1", n)
			}
		})
	}
}

func TestDevicePollContinuesWhenNotSent(t *testing.T) {
	srv := xaitest.New(t)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	var dials atomic.Int32
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) == 3 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	old := authClient.Transport
	authClient.Transport = tr
	t.Cleanup(func() { authClient.Transport = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stubWait(t, cancel, 5)

	set, _ := readSettings(srv.Settings())
	g, err := deviceLogin(ctx, set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	if err != nil || g == nil || g.Credential.AccessToken == "" {
		t.Fatalf("login after a refused poll: grant %+v, err %v", g, err)
	}
	if n := dials.Load(); n < 4 {
		t.Errorf("%d connections, want a poll after the refused one", n)
	}
}
