package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestParseConfigPreparesManagementKey(t *testing.T) {
	c, err := ParseConfig([]byte(`{"listen": "127.0.0.1:0", "client_keys": ["client-a"], "management_key": "m", "extension": {}}`), "/srv/pluxy/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.managementDigest == nil {
		t.Error("no management key digest after loading a config with a management key")
	}
}

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
		from := fmt.Sprintf("198.51.%d.%d", i>>8, i&255)
		start := time.Now()
		got := managementCall(m, from, "x")
		d := time.Since(start)
		if got != http.StatusUnauthorized {
			t.Fatalf("a wrong token answered %d, want 401", got)
		}
		return d
	}
	timeIt(short)
	timeIt(long)
	fastShort, fastLong := time.Duration(1<<63-1), time.Duration(1<<63-1)
	for range 10 {
		fastShort, fastLong = min(fastShort, timeIt(short)), min(fastLong, timeIt(long))
	}
	if fastLong > fastShort+2*time.Millisecond {
		t.Errorf("a check took %v against a 1-byte key and %v against a 16 MiB key: the time shows the key's length", fastShort, fastLong)
	}
}
