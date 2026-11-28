package server

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

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

func TestAuthorizedDoesNotShowKeyLength(t *testing.T) {
	prepared := func(n int) *Config {
		pad := strings.Repeat("k", n-1)
		cfg := &Config{ClientKeys: []string{pad + "1", pad + "2", pad + "3", pad + "4"}}
		cfg.prepareClientKeys()
		return cfg
	}
	replace := func(cfg *Config) {
		pad := strings.Repeat("z", len(cfg.ClientKeys[0])-1)
		cfg.ClientKeys = []string{pad + "5", pad + "6", pad + "7", pad + "8"}
	}
	check := func(t *testing.T, cfg *Config, token string) (bool, []int) {
		t.Helper()
		var hashed []int
		cfg.keyHash = func(p []byte) [sha256.Size]byte {
			hashed = append(hashed, len(p))
			return sha256.Sum256(p)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return authorized(cfg, r, false), hashed
	}
	for _, tc := range []struct {
		name   string
		keys   int
		keyed  string
		accept bool
		other  string
	}{
		{"1-byte keys", 1, "1", true, "5"},
		{"4 MiB keys", 4 << 20, strings.Repeat("k", 4<<20-1) + "1", true, strings.Repeat("z", 4<<20-1) + "5"},
		{"4 MiB keys, 1-byte token", 4 << 20, "x", false, "y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := prepared(tc.keys)
			for _, c := range []struct {
				token string
				want  bool
			}{
				{tc.keyed, tc.accept},
				{tc.other, false},
			} {
				got, hashed := check(t, cfg, c.token)
				if got != c.want {
					t.Errorf("token of %d bytes: authorized = %v, want %v", len(c.token), got, c.want)
				}
				if want := []int{len(c.token)}; !slices.Equal(hashed, want) {
					t.Errorf("token of %d bytes: the check hashed %v bytes, want %v: nothing of the keys may be hashed", len(c.token), hashed, want)
				}
			}
			replace(cfg)
			got, hashed := check(t, cfg, tc.keyed)
			if got != tc.accept {
				t.Errorf("after the keys were replaced: authorized = %v, want %v, which is what the prepared keys' digests give", got, tc.accept)
			}
			if want := []int{len(tc.keyed)}; !slices.Equal(hashed, want) {
				t.Errorf("after the keys were replaced: the check hashed %v bytes, want %v", hashed, want)
			}
			if got, _ := check(t, cfg, tc.other); got {
				t.Error("after the keys were replaced: authorized a token only one of the new keys is: the check reads the config's keys, not their digests")
			}
		})
	}
}

func TestParseConfigPreparesClientKeys(t *testing.T) {
	c, err := ParseConfig([]byte(`{"listen": "127.0.0.1:0", "client_keys": ["client-a", "client-b"], "management_key": "m", "extension": {}}`), "/srv/pluxy/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.keyDigests) != 2 {
		t.Errorf("%d key digests after loading two client keys, want 2", len(c.keyDigests))
	}
}
