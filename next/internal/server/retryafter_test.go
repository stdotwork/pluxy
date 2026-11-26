package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Retry-After is whole seconds rounded up, so it agrees with the "try again in" in the message:
// 0.2s left is 1, 1s is 1, 1.2s is 2.
func TestRetryAfterRoundsUp(t *testing.T) {
	for _, c := range []struct {
		ms   int64
		want string
	}{{1, "1"}, {200, "1"}, {999, "1"}, {1000, "1"}, {1001, "2"}, {1200, "2"}, {2000, "2"}, {59900, "60"}} {
		w := httptest.NewRecorder()
		clientFormat{}.writeError(w, &llm.Error{Code: llm.CodeRateLimit, Status: http.StatusTooManyRequests, RetryAfterMS: c.ms})
		if got := w.Header().Get("Retry-After"); got != c.want {
			t.Errorf("retry_after_ms %d: Retry-After %q, want %q", c.ms, got, c.want)
		}
	}
	w := httptest.NewRecorder()
	clientFormat{}.writeError(w, &llm.Error{Code: llm.CodeRateLimit, Status: http.StatusTooManyRequests})
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Errorf("no wait given: Retry-After %q, want none", got)
	}
}

// A locked-out management client is told the wait in whole seconds rounded up, in the header
// and in the message alike, down to the last fraction of a second.
func TestManagementLockoutWaitRoundsUp(t *testing.T) {
	cfg := &Config{ManagementKey: "right-key", ManagementAllowRemote: true}
	m := NewManagement(func() *Config { return cfg }, nil, nil)
	now := time.Unix(1_700_000_000, 0)
	m.lockout.now = func() time.Time { return now }
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, ManagementPrefix+"/ping", nil)
		r.RemoteAddr = "198.51.100.7:1234"
		r.Header.Set("Authorization", "Bearer wrong")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		return w
	}
	for range lockoutFailures {
		call()
	}
	for _, c := range []struct {
		left time.Duration
		want int
	}{{200 * time.Millisecond, 1}, {time.Second, 1}, {1200 * time.Millisecond, 2}} {
		now = time.Unix(1_700_000_000, 0).Add(lockoutFor - c.left)
		w := call()
		msg := "too many failed management keys from this address; try again in " + (time.Duration(c.want) * time.Second).String()
		if got := w.Header().Get("Retry-After"); got != strconv.Itoa(c.want) || !strings.Contains(w.Body.String(), msg) {
			t.Errorf("%v left: Retry-After %q, body %s; want %d and %q", c.left, got, w.Body, c.want, msg)
		}
	}
}
