package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/meta/metatest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

// A renewal's error is retryable only when the request never reached Meta, or when Meta rate
// limited it.
func TestMintRetryOnlyWhenNotSent(t *testing.T) {
	cases := []struct {
		name      string
		answer    func(w http.ResponseWriter, r *http.Request) bool
		closed    bool
		code      string
		retryable bool
	}{
		{name: "connection refused", closed: true, code: llm.CodeUnavailable, retryable: true},
		{name: "hung up after the request", answer: func(w http.ResponseWriter, r *http.Request) bool { sdktest.HangUp(w); return true },
			code: llm.CodeUnavailable},
		{name: "503", answer: func(w http.ResponseWriter, r *http.Request) bool {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return true
		}, code: llm.CodeUnavailable},
		{name: "reply broke off", answer: func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"api_key":`)
			sdktest.HangUp(w)
			return true
		}, code: llm.CodeUnavailable},
		{name: "429", answer: func(w http.ResponseWriter, r *http.Request) bool {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return true
		}, code: llm.CodeRateLimit, retryable: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := metatest.New(t)
			srv.Answer = c.answer
			set, _ := readSettings(srv.Settings())
			if c.closed {
				set.MintURL = sdktest.ClosedURL(t) + "/muse-code/key"
			}
			_, err := mint(context.Background(), set, "dca:x")
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}

// The first call of a device login has the same rule.
func TestDeviceStartRetryOnlyWhenNotSent(t *testing.T) {
	srv := metatest.New(t)
	set, _ := readSettings(srv.Settings())
	set.AuthBaseURL = sdktest.ClosedURL(t)
	_, err := deviceLogin(context.Background(), set, promptFunc(func(protocol.Prompt) {}))
	sdktest.WantError(t, err, llm.CodeUnavailable, true)

	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool { sdktest.HangUp(w); return true }
	set, _ = readSettings(srv.Settings())
	_, err = deviceLogin(context.Background(), set, promptFunc(func(protocol.Prompt) {}))
	sdktest.WantError(t, err, llm.CodeUnavailable, false)
}

// A poll that fails after it was sent stops the login.
func TestPollStopsAfterSendFailure(t *testing.T) {
	srv := metatest.New(t)
	var polls atomic.Int32
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oidc/device/token/" {
			return false
		}
		polls.Add(1)
		sdktest.HangUp(w)
		return true
	}
	set, _ := readSettings(srv.Settings())
	_, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	sdktest.WantError(t, err, llm.CodeUnavailable, false)
	if polls.Load() != 1 {
		t.Errorf("%d polls reached Meta, want 1", polls.Load())
	}
}

// A poll whose request never left this machine is tried again at the next interval.
func TestPollContinuesWhenNotSent(t *testing.T) {
	srv := metatest.New(t)
	// Every request opens its own connection; the second connection (the first poll) is refused.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	var dials atomic.Int32
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) == 2 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	old := authClient.Transport
	authClient.Transport = tr
	defer func() { authClient.Transport = old }()
	oldWait := waitPoll
	waitPoll = func(ctx context.Context, d time.Duration) bool { return ctx.Err() == nil }
	defer func() { waitPoll = oldWait }()

	set, _ := readSettings(srv.Settings())
	g, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	if err != nil || g.Credential.AccessToken == "" {
		t.Fatalf("login after a refused poll: grant %+v, err %v", g, err)
	}
	if dials.Load() < 3 {
		t.Errorf("%d connections, want a poll after the refused one", dials.Load())
	}
}
