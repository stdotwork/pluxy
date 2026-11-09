package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthorizedMatchesClientKeys(t *testing.T) {
	cfg := &Config{ClientKeys: []string{"client-a", "client-b"}}
	for _, tc := range []struct {
		token string
		want  bool
	}{
		{"client-a", true},
		{"client-b", true},
		{"client-", false},
		{"client-ab", false},
		{"client-c", false},
		{"", false},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if got := authorized(cfg, r, false); got != tc.want {
			t.Errorf("token %q: authorized = %v, want %v", tc.token, got, tc.want)
		}
	}
}

// The check must not take less time for a token whose length differs from the key's: a
// comparison that returns at once on a length mismatch tells a client how long a key is.
// The key here is large enough that the difference is not measurement noise: comparing the two
// strings directly takes milliseconds for a token of the key's length and nanoseconds for a
// short one, while comparing digests hashes the key either way.
func TestAuthorizedTakesTheSameTimeWhateverTheTokenLength(t *testing.T) {
	key := strings.Repeat("k", 8<<20)
	cfg := &Config{ClientKeys: []string{key}}
	request := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	sameLength, short := request(strings.Repeat("x", len(key))), request("x")
	fastest := func(r *http.Request) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range 5 {
			start := time.Now()
			if authorized(cfg, r, false) {
				t.Fatal("a wrong token was authorized")
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	a, b := fastest(sameLength), fastest(short)
	// With digests the short token costs about half the same-length one (one hash of the key
	// against two hashes); a direct comparison costs a thousandth or less.
	if b < a/5 {
		t.Errorf("a short wrong token was checked in %v, a same-length one in %v: the check reveals the key's length", b, a)
	}
}
