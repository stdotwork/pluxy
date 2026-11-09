package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The tokens each configured key accepts: exactly those, not a prefix or an extension of one.
func TestAuthorizedMatchesClientKeys(t *testing.T) {
	cfg := &Config{ClientKeys: []string{"client-a", "client-b"}}
	for _, prepared := range []bool{false, true} {
		if prepared {
			cfg.prepareClientKeys()
		}
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
				t.Errorf("prepared %v, token %q: authorized = %v, want %v", prepared, tc.token, got, tc.want)
			}
		}
	}
}

// How long a check takes must not show how long a configured key is. The same wrong token is
// checked against a config with a short key and one with a huge key. A comparison of the raw
// strings takes milliseconds when the lengths are equal and nanoseconds when they differ; hashing
// the configured key on each request takes time that grows with its length. The check here takes
// the same time either way: it hashes the token and compares fixed-size digests prepared when the
// config was loaded. The key is large enough that the gap is not measurement noise.
func TestAuthorizedTimeDoesNotShowKeyLength(t *testing.T) {
	huge := strings.Repeat("k", 8<<20)
	request := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	token := request(strings.Repeat("x", len(huge)))
	fastest := func(key string) time.Duration {
		cfg := &Config{ClientKeys: []string{key}}
		cfg.prepareClientKeys()
		best := time.Duration(1<<63 - 1)
		for range 5 {
			start := time.Now()
			if authorized(cfg, token, false) {
				t.Fatal("a wrong token was authorized")
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	short, long := fastest("k"), fastest(huge)
	// Equal up to noise; a raw comparison differs by a factor of 10^4 or more, and hashing the
	// key on each request makes the long key cost about twice the short one.
	if short < long*7/10 || long < short*7/10 {
		t.Errorf("checking an 8 MiB token took %v against a 1-byte key and %v against an 8 MiB key: the time shows the key's length", short, long)
	}
}

// A loaded config has its key digests made, so a request's check does not hash the keys.
func TestParseConfigPreparesClientKeys(t *testing.T) {
	c, err := ParseConfig([]byte(`{"listen": "127.0.0.1:0", "client_keys": ["client-a", "client-b"], "management_key": "m", "extension": {}}`), "/srv/pluxy/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.keyDigests) != 2 {
		t.Errorf("%d key digests after loading two client keys, want 2", len(c.keyDigests))
	}
}
