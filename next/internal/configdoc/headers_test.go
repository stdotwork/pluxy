package configdoc

import (
	"slices"
	"strings"
	"testing"
)

// The placeholder GET /config shows for a secret header value is the one GET /accounts shows for
// it (Redactor.Headers on the header core read): for text, a number and text with spaces around
// it in a provider section of a config.yaml, which core reads as text without the spaces, and
// for an account's headers in core's own layout, read as written. Restore puts each back as it
// was, also a number and a string that read as the same header text.
func TestConfigHeaderPlaceholdersAreThoseOfAccounts(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		isYAML    bool
	}{
		{"provider section", `listen: 127.0.0.1:8317
access:
  api-keys: [fake-client-key-1]
codex-api-key:
  - api-key: fake-codex-key-1
    base-url: https://codex.example.com
    headers:
      X-Api-Key: 12345678
      X-Auth-Float: 1e3
      Authorization: "  Bearer fake-padded-token  "
      X-Session-Token: fake-plain-token
api-keys:
  claude:
    - base-url: https://claude.example.com
      headers:
        X-Gateway-Key: 0x1F
      keys:
        - api-key: fake-claude-key-1
password: "12345678"
`, true},
		{"core's layout", `listen: 127.0.0.1:8317
access:
  api-keys: [fake-client-key-1]
extensions:
  pluxy/x:
    accounts:
      - id: a
        headers:
          X-Api-Key: "  fake-kept-spaces  "
          Authorization: Bearer fake-yaml-bearer
`, true},
		{"core's layout, json", `{"listen": "127.0.0.1:8317", "client_keys": ["fake-client-key-1"],
  "extensions": {"pluxy/x": {"accounts": [{"id": "a", "headers": {"X-Api-Key": "  fake-json-spaces  "}}]}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loads(t, tc.src, tc.isYAML)
			r := NewRedactor()
			red, err := r.Redact([]byte(tc.src), tc.isYAML)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, a := range cfg.Accounts() {
				for name, shown := range r.Headers(a.Headers) {
					if shown == a.Headers[name] {
						continue // not a secret header
					}
					n++
					at := name + ": " + shown
					if !tc.isYAML {
						at = `"` + name + `": "` + shown + `"`
					}
					if !strings.Contains(string(red), at) {
						t.Errorf("account %s header %s (%q): GET /accounts shows %s, which GET /config does not hold:\n%s", a.ID, name, a.Headers[name], shown, red)
					}
				}
			}
			if n == 0 {
				t.Fatal("no secret header was read")
			}
			back, err := r.Restore(red, []byte(tc.src), tc.isYAML)
			if err != nil || string(back) != tc.src {
				t.Errorf("Restore: %v\n%s", err, back)
			}
		})
	}
}

// A header named like a passphrase is a secret header everywhere, as GET /config reads it: GET
// /accounts (Redactor.Headers) shows the placeholder GET /config shows in its place, and the
// header rule extensions and the request log use (upheader.Secret) counts it.
func TestPassphraseHeaderIsSecret(t *testing.T) {
	const value = "fake-header-passphrase-1"
	src := "listen: 127.0.0.1:0\naccess:\n  api-keys: [fake-client-key-1]\nextensions:\n  pluxy/x:\n    accounts:\n      - id: a\n        headers:\n          X-Passphrase: " + value + "\n"
	r := NewRedactor()
	red, err := r.Redact([]byte(src), true)
	if err != nil {
		t.Fatal(err)
	}
	shown := r.Headers(map[string]string{"X-Passphrase": value})["X-Passphrase"]
	if shown != r.Placeholder(value) || !strings.Contains(string(red), "X-Passphrase: "+shown) {
		t.Errorf("GET /accounts shows %q; GET /config:\n%s", shown, red)
	}
}

// A key named like a secret holds a secret whatever its shape, also when its name says "header":
// header_token, auth_header_secret, header_api_key. Its value is hidden in GET /config and GET
// /accounts and taken out of text (Doc.TextSecrets, SettingsSecrets), as the value of any other
// secret key is. A real mapping of headers (extra_headers) keeps the headers rule: only its secret
// headers are hidden.
func TestHeaderNamedSecretKeysAreSecrets(t *testing.T) {
	const keep = "fake-visible-trace-1"
	for _, tc := range []struct{ name, key, value, secret string }{
		{"scalar", "header_token", `"fake-header-token-1"`, "fake-header-token-1"},
		{"prefixed", "auth_header_token", `"fake-header-token-2"`, "fake-header-token-2"},
		{"list", "header_secret", `["fake-header-secret-3"]`, "fake-header-secret-3"},
		{"mapping", "header_api_key", `{"value": "fake-header-secret-4"}`, "fake-header-secret-4"},
	} {
		settings := `{"` + tc.key + `": ` + tc.value + `, "extra_headers": {"X-Trace": "` + keep + `", "X-Api-Key": "fake-extra-key-5"}}`
		t.Run(tc.name, func(t *testing.T) {
			out, err := NewRedactor().RedactSettings([]byte(settings))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), tc.secret) || strings.Contains(string(out), "fake-extra-key-5") || !strings.Contains(string(out), keep) {
				t.Errorf("RedactSettings = %s, want %s and the secret header hidden and X-Trace kept", out, tc.secret)
			}
			if got := SettingsSecrets([]byte(settings)); !slices.Contains(got, tc.secret) || slices.Contains(got, keep) {
				t.Errorf("SettingsSecrets = %q, want it to hold %s and not %s", got, tc.secret, keep)
			}
			cfg := `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "extension": {"accounts": [{"id": "a", "settings": ` + settings + `}]}}`
			if got := mustParse(t, cfg, false).TextSecrets(); !slices.Contains(got, tc.secret) || slices.Contains(got, keep) {
				t.Errorf("TextSecrets = %q, want it to hold %s and not %s", got, tc.secret, keep)
			}
			red, err := NewRedactor().Redact([]byte(cfg), false)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(red), tc.secret) {
				t.Errorf("GET /config shows %s:\n%s", tc.secret, red)
			}
		})
	}
}
