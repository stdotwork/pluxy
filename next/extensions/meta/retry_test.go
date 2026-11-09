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
		{name: "hung up after the request", answer: func(w http.ResponseWriter, r *http.Request) bool { hangUp(w); return true },
			code: llm.CodeUnavailable},
		{name: "503", answer: func(w http.ResponseWriter, r *http.Request) bool {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return true
		}, code: llm.CodeUnavailable},
		{name: "reply broke off", answer: func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"api_key":`)
			hangUp(w)
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
				set.MintURL = closedURL(t) + "/muse-code/key"
			}
			_, err := mint(context.Background(), set, "dca:x")
			wantError(t, err, c.code, c.retryable)
		})
	}
}

// The first call of a device login has the same rule.
func TestDeviceStartRetryOnlyWhenNotSent(t *testing.T) {
	srv := metatest.New(t)
	set, _ := readSettings(srv.Settings())
	set.AuthBaseURL = closedURL(t)
	_, err := deviceLogin(context.Background(), set, promptFunc(func(protocol.Prompt) {}))
	wantError(t, err, llm.CodeUnavailable, true)

	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool { hangUp(w); return true }
	set, _ = readSettings(srv.Settings())
	_, err = deviceLogin(context.Background(), set, promptFunc(func(protocol.Prompt) {}))
	wantError(t, err, llm.CodeUnavailable, false)
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
		hangUp(w)
		return true
	}
	set, _ := readSettings(srv.Settings())
	_, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	wantError(t, err, llm.CodeUnavailable, false)
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
