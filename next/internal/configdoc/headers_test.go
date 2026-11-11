package configdoc

import (
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
