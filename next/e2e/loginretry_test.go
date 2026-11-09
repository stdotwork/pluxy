package e2e

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
	"github.com/stdotwork/pluxy/next/llm"
)

// closedPort returns the address of a port nothing listens on.
func closedPort(t *testing.T) string {
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

// failedLogin starts a device login of account id against the provider at baseURL and returns the
// error it ends with.
func failedLogin(t *testing.T, base, baseURL, id string) *llm.Error {
	t.Helper()
	var l struct {
		Login struct {
			ID    string     `json:"id"`
			State string     `json:"state"`
			Error *llm.Error `json:"error"`
		} `json:"login"`
	}
	if code := manage(t, base, "POST", "/accounts", map[string]any{
		"id": id, "method": "device", "settings": map[string]string{"base_url": baseURL},
	}, &l); code != http.StatusAccepted {
		t.Fatalf("starting the login: status %d, %+v", code, l.Login)
	}
	deadline := time.Now().Add(15 * time.Second)
	for l.Login.State == "pending" {
		if time.Now().After(deadline) {
			t.Fatal("the login did not finish")
		}
		time.Sleep(50 * time.Millisecond)
		manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &l)
	}
	if l.Login.State != "failed" || l.Login.Error == nil {
		t.Fatalf("login ended %s with error %+v, want failed", l.Login.State, l.Login.Error)
	}
	return l.Login.Error
}

// A failed login says, in `retryable`, whether the request never reached the provider: a refused
// connection is retryable; a 503, and a connection closed after the request was read, are not.
func TestLoginErrorRetryableOnlyWhenNotSent(t *testing.T) {
	base, _ := startOAuthCore(t)
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	hungUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hangUp(w) }))
	defer hungUp.Close()

	for i, c := range []struct {
		name, url string
		status    int
		retryable bool
	}{
		{"connection refused", closedPort(t), 0, true},
		{"503", unavailable.URL, http.StatusServiceUnavailable, false},
		{"closed after the request", hungUp.URL, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := failedLogin(t, base, c.url, fmt.Sprintf("acct-%d", i))
			if e.Code != llm.CodeUnavailable || e.Retryable != c.retryable || e.Status != c.status {
				t.Errorf("login error = %+v, want code unavailable, status %d, retryable %v", e, c.status, c.retryable)
			}
		})
	}
}

// proxyTo returns a server that passes every request to target, and closes the connection of
// every request instead while hang is set.
func proxyTo(t *testing.T, target string, hang *atomic.Bool) *httptest.Server {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hang.Load() {
			hangUp(w)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A failed renewal shows in the account list as `renewal_error`, with the same meaning of
// `retryable`: a refused connection is retryable; a 503 and a connection closed after the request
// was read are not.
func TestRenewalErrorRetryableOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		fail      func(srv *oauthtest.Server, proxy *httptest.Server, hang *atomic.Bool)
		status    int
		retryable bool
	}{
		{"connection refused", func(_ *oauthtest.Server, proxy *httptest.Server, _ *atomic.Bool) { proxy.Close() }, 0, true},
		{"503", nil, http.StatusServiceUnavailable, false},
		{"closed after the request", func(_ *oauthtest.Server, _ *httptest.Server, hang *atomic.Bool) { hang.Store(true) }, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := oauthtest.New(t)
			if c.fail == nil {
				// Only renewals fail; the login below still works.
				srv.RefreshFailure = "unavailable"
			}
			var hang atomic.Bool
			proxy := proxyTo(t, srv.URL, &hang)
			base, _ := startOAuthCore(t)
			deviceLoginAt(t, base, proxy.URL, srv, "work")
			if c.fail != nil {
				c.fail(srv, proxy, &hang)
			}

			if code := manage(t, base, "POST", "/accounts/work/refresh", nil, nil); code != http.StatusBadGateway {
				t.Fatalf("refresh: status %d, want 502", code)
			}
			var list struct {
				Accounts []struct {
					ID           string     `json:"id"`
					RenewalError *llm.Error `json:"renewal_error"`
				} `json:"accounts"`
			}
			manage(t, base, "GET", "/accounts", nil, &list)
			if len(list.Accounts) != 1 || list.Accounts[0].RenewalError == nil {
				t.Fatalf("accounts = %+v, want one with a renewal error", list.Accounts)
			}
			e := list.Accounts[0].RenewalError
			if e.Code != llm.CodeUnavailable || e.Retryable != c.retryable || e.Status != c.status {
				t.Errorf("renewal error = %+v, want code unavailable, status %d, retryable %v", e, c.status, c.retryable)
			}
		})
	}
}

// deviceLoginAt adds account id by device login against the provider reached at baseURL, approving
// the code at the fake provider srv behind it.
func deviceLoginAt(t *testing.T, base, baseURL string, srv *oauthtest.Server, id string) {
	t.Helper()
	var l loginReply
	if code := manage(t, base, "POST", "/accounts", map[string]any{
		"id": id, "method": "device", "settings": map[string]string{"base_url": baseURL},
	}, &l); code != http.StatusAccepted || l.Login.Prompt == nil || l.Login.Prompt.UserCode == "" {
		t.Fatalf("starting the login: status %d, %+v", code, l.Login)
	}
	srv.ApproveDevice(l.Login.Prompt.UserCode)
	deadline := time.Now().Add(15 * time.Second)
	for l.Login.State == "pending" {
		if time.Now().After(deadline) {
			t.Fatal("the login did not finish")
		}
		time.Sleep(100 * time.Millisecond)
		manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &l)
	}
	if l.Login.State != "done" {
		t.Fatalf("login ended %s: %+v", l.Login.State, l.Login.Error)
	}
}
