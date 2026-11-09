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

// How long a check takes must not show how long the configured keys are. The same wrong token is
// checked against a config with four 1-byte keys and one with four 4 MiB keys, by a 1-byte token
// and by a 4 MiB one. Hashing each configured key on each request (an earlier version of the
// check) costs tens of milliseconds for the long keys against microseconds for the short ones when
// the token is short; comparing the raw strings (the version before) takes milliseconds for the
// long keys when the token is as long and nanoseconds for the short ones. This check hashes only
// the token, once, and compares digests prepared when the config was loaded, so the keys' length
// costs nothing. The two sides run alternately and each keeps its fastest of ten, so a loaded
// machine's noise falls on both; only a long-key check clearly slower than the short-key one fails.
func TestAuthorizedTimeDoesNotShowKeyLength(t *testing.T) {
	// Four keys of n bytes each, the token's length when it is n bytes long.
	prepared := func(n int) *Config {
		pad := strings.Repeat("k", n-1)
		cfg := &Config{ClientKeys: []string{pad + "1", pad + "2", pad + "3", pad + "4"}}
		cfg.prepareClientKeys()
		return cfg
	}
	short, long := prepared(1), prepared(4<<20)
	for _, tc := range []struct {
		name  string
		token string
		slow  func(short, long time.Duration) bool
	}{
		{"1-byte token", "x", func(short, long time.Duration) bool { return long > short+5*time.Millisecond }},
		{"4 MiB token", strings.Repeat("x", 4<<20), func(short, long time.Duration) bool { return long > 2*short }},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		timeIt := func(cfg *Config) time.Duration {
			start := time.Now()
			if authorized(cfg, r, false) {
				t.Fatal("a wrong token was authorized")
			}
			return time.Since(start)
		}
		timeIt(short) // warm up
		fastShort, fastLong := time.Duration(1<<63-1), time.Duration(1<<63-1)
		for range 10 {
			fastShort, fastLong = min(fastShort, timeIt(short)), min(fastLong, timeIt(long))
		}
		if tc.slow(fastShort, fastLong) {
			t.Errorf("%s: a check took %v against four 1-byte keys and %v against four 4 MiB keys: the time shows the keys' length", tc.name, fastShort, fastLong)
		}
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
