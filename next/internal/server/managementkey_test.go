package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// managementCall sends GET /ping to m with token as the bearer token (none when empty), from the
// client address from, and returns the status.
func managementCall(m *Management, from, token string) int {
	r := httptest.NewRequest(http.MethodGet, ManagementPrefix+"/ping", nil)
	r.RemoteAddr = from + ":1234"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w.Code
}

func pingManagement(cfg *Config) *Management {
	m := NewManagement(func() *Config { return cfg }, nil, nil)
	m.Handle("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return m
}

// The token the management key accepts: exactly it, not a prefix or an extension of it, and a
// client that sends five wrong ones is locked out, with the key's digest made at load or not.
func TestManagementKeyMatches(t *testing.T) {
	loaded, err := ParseConfig([]byte(`{"listen": "127.0.0.1:0", "client_keys": ["c"], "management_key": "right-key", "management_allow_remote": true, "extension": {}}`), "/srv/pluxy/config.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]*Config{
		"loaded":        loaded,
		"built by hand": {ManagementKey: "right-key", ManagementAllowRemote: true},
	} {
		t.Run(name, func(t *testing.T) {
			m := pingManagement(cfg)
			for token, want := range map[string]int{
				"right-key":  http.StatusNoContent,
				"right-ke":   http.StatusUnauthorized,
				"right-keyy": http.StatusUnauthorized,
				"wrong-key":  http.StatusUnauthorized,
				"":           http.StatusUnauthorized,
			} {
				if got := managementCall(m, "198.51.100.1", token); got != want {
					t.Errorf("token %q answered %d, want %d", token, got, want)
				}
			}
			for range lockoutFailures {
				managementCall(m, "198.51.100.2", "wrong-key")
			}
			if got := managementCall(m, "198.51.100.2", "right-key"); got != http.StatusForbidden {
				t.Errorf("a client that failed %d times, with the right key: %d, want 403", lockoutFailures, got)
			}
		})
	}
}

// A loaded config has the digest of its management key made, so a request's check does not hash
// the key.
func TestParseConfigPreparesManagementKey(t *testing.T) {
	c, err := ParseConfig([]byte(`{"listen": "127.0.0.1:0", "client_keys": ["client-a"], "management_key": "m", "extension": {}}`), "/srv/pluxy/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.managementDigest == nil {
		t.Error("no management key digest after loading a config with a management key")
	}
}

// How long a management key check takes must not show how long the management key is. The same
// wrong 1-byte token is checked against a config with a 1-byte key and one with a 16 MiB key.
// Hashing the key on each request (the check before) costs milliseconds for the long key against
// microseconds for the short one; this check hashes only the token, and compares it with the
// digest made when the config was loaded. The two sides run alternately and each keeps its
// fastest of ten, so a loaded machine's noise falls on both; only a long-key check clearly slower
// than the short-key one fails.
func TestManagementKeyTimeDoesNotShowKeyLength(t *testing.T) {
	loaded := func(n int) *Management {
		cfg := &Config{ManagementKey: strings.Repeat("k", n), ManagementAllowRemote: true}
		cfg.prepareManagementKey()
		return pingManagement(cfg)
	}
	short, long := loaded(1), loaded(16<<20)
	i := 0
	timeIt := func(m *Management) time.Duration {
		i++
		from := fmt.Sprintf("198.51.%d.%d", i>>8, i&255) // a new client each time: none is locked out
		start := time.Now()
		got := managementCall(m, from, "x")
		d := time.Since(start)
		if got != http.StatusUnauthorized {
			t.Fatalf("a wrong token answered %d, want 401", got)
		}
		return d
	}
	timeIt(short) // warm up
	timeIt(long)
	fastShort, fastLong := time.Duration(1<<63-1), time.Duration(1<<63-1)
	for range 10 {
		fastShort, fastLong = min(fastShort, timeIt(short)), min(fastLong, timeIt(long))
	}
	if fastLong > fastShort+2*time.Millisecond {
		t.Errorf("a check took %v against a 1-byte key and %v against a 16 MiB key: the time shows the key's length", fastShort, fastLong)
	}
}
