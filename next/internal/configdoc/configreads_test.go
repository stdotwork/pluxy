package configdoc

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/protocol"
)

// redactRoundTrip redacts src, checks that no secret is left as a value where a secret was and
// that each value the test names is still shown, and that Restore gives src back byte for byte.
func redactRoundTrip(t *testing.T, src string, isYAML bool, secrets, shown []string) string {
	t.Helper()
	r := NewRedactor()
	red, err := r.Redact([]byte(src), isYAML)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	for _, s := range secrets {
		if strings.Contains(string(red), s) {
			t.Errorf("the redacted document shows %q:\n%s", s, red)
		}
	}
	for _, s := range shown {
		if !strings.Contains(string(red), s) {
			t.Errorf("the redacted document lacks %q:\n%s", s, red)
		}
	}
	back, err := r.Restore(red, []byte(src), isYAML)
	if err != nil || string(back) != src {
		t.Errorf("Restore: %v\n%s", err, back)
	}
	return string(red)
}

// A value below a secret key that is also written in plain elsewhere, such as a 3, is hidden
// where it is a secret and shown where it is not; the document can still be read.
func TestRedactSecretValueAlsoPlainElsewhere(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		src := `{"listen": "127.0.0.1:8317", "client_keys": ["fake-client-key-1"],
  "extension": {"manifest": "x.json", "restart": {"quarantine_after": 3},
    "accounts": [{"id": "a", "settings": {"client_secret": {"value": "fake-nested-secret-1", "rotate_after_days": 3}}}]}}
`
		red := redactRoundTrip(t, src, false, []string{"fake-nested-secret-1"}, []string{`"quarantine_after": 3`})
		if strings.Contains(red, `"rotate_after_days": 3`) {
			t.Errorf("the secret 3 is shown:\n%s", red)
		}
	})
	t.Run("yaml", func(t *testing.T) {
		src := "request-retry: 3\nbedrock-api-key:\n  - api-key: fake-bedrock-key-1\n    priority: 3\n"
		red := redactRoundTrip(t, src, true, []string{"fake-bedrock-key-1"}, []string{"request-retry: 3\n"})
		if strings.Contains(red, "priority: 3") {
			t.Errorf("the secret 3 is shown:\n%s", red)
		}
	})
}

// Scrub leaves a secret shorter than MinTextSecret in a message, as substring redaction does.
func TestScrubLeavesShortSecrets(t *testing.T) {
	r := NewRedactor()
	got := r.Scrub("retry out of range 1-3, key fake-scrub-secret-1", []string{"3", "1234567", "fake-scrub-secret-1"})
	if want := "retry out of range 1-3, key " + r.Placeholder("fake-scrub-secret-1"); got != want {
		t.Errorf("Scrub = %q, want %q", got, want)
	}
}

// A key written as an alias of an anchored name is read as that name, wherever names are read:
// a provider entry's api-key, a management secret-key, and a header name.
func TestRedactAliasKeys(t *testing.T) {
	for _, tc := range []struct{ name, src, secret string }{
		{"api-key", "names: [&k api-key]\ncodex-api-key:\n  - {*k : fake-alias-key-secret-1, base-url: https://codex.example.com}\n", "fake-alias-key-secret-1"},
		{"secret-key", "names: [&m secret-key]\nremote-management: {*m : fake-alias-management-1}\n", "fake-alias-management-1"},
		{"header", "names: [&h Authorization]\ncodex-api-key:\n  - api-key: fake-codex-key-1\n    base-url: https://codex.example.com\n    headers: {*h : Bearer fake-alias-header-1}\n", "fake-alias-header-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mustParse(t, tc.src, true)
			if !slices.ContainsFunc(d.Secrets(), func(s string) bool { return strings.Contains(s, tc.secret) }) {
				t.Errorf("Secrets() = %q, want one holding %q", d.Secrets(), tc.secret)
			}
			redactRoundTrip(t, tc.src, true, []string{tc.secret}, nil)
		})
	}
}

// A secret with an explicit tag is shown as a placeholder and goes back with its tag, so core
// reads the same value: !!int 0x1F2E3D4C stays the number 523124044. A file without anchors is
// restored in place, byte for byte; one with an anchored secret is written out again.
func TestRestoreKeepsExplicitTags(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		exact     bool
	}{
		{"in place", "api-key: !!int 0x1F2E3D4C\nsecret-key: !!binary c2stbGl2ZS1zZWNyZXQ=\nkept: 1\n", true},
		{"written out again", "api-key: !!int 0x1F2E3D4C\nsecret-key: !!binary c2stbGl2ZS1zZWNyZXQ=\nprivate-key: &p !!int 0x7FFFFFFF\nkept: 1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRedactor()
			red, err := r.Redact([]byte(tc.src), true)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(red), "0x1F2E3D4C") || strings.Contains(string(red), "c2stbGl2ZS1zZWNyZXQ=") || strings.Contains(string(red), "0x7FFFFFFF") {
				t.Fatalf("the redacted document shows a secret:\n%s", red)
			}
			back, err := r.Restore(red, []byte(tc.src), true)
			if err != nil {
				t.Fatal(err)
			}
			if tc.exact && string(back) != tc.src {
				t.Errorf("not restored byte for byte:\n%s", back)
			}
			var got map[string]any
			if err := yaml.Unmarshal(back, &got); err != nil {
				t.Fatal(err)
			}
			if got["api-key"] != 523124044 || got["secret-key"] != "sk-live-secret" || !tc.exact && got["private-key"] != 2147483647 {
				t.Errorf("restored values %#v, want 523124044, \"sk-live-secret\" and 2147483647:\n%s", got, back)
			}
		})
	}
}

// The parts of a URL in settings are removed from text on their own as well: its password, its
// user name and password together, in Basic form too, and each query value of 8 bytes or more.
func TestSettingsSecretsHoldURLParts(t *testing.T) {
	got := SettingsSecrets([]byte(`{"base_url": "https://gw-user:fake-gw-pass-1234@up.example/v1?token=fake-url-token-1234&v=2"}`))
	for _, want := range []string{
		"https://gw-user:fake-gw-pass-1234@up.example/v1?token=fake-url-token-1234&v=2",
		"fake-gw-pass-1234",
		"gw-user:fake-gw-pass-1234",
		"fake-url-token-1234",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("SettingsSecrets lacks %q: %q", want, got)
		}
	}
	if slices.Contains(got, "2") || slices.Contains(got, "gw-user") {
		t.Errorf("SettingsSecrets holds a value shorter than 8 bytes: %q", got)
	}
	// As core removes them from text (host and request log), the Basic form included.
	data, _ := json.Marshal(got)
	r := protocol.NewRedactor(&protocol.Credential{Kind: "settings", Data: data})
	basic := base64.StdEncoding.EncodeToString([]byte("gw-user:fake-gw-pass-1234"))
	text := "pass fake-gw-pass-1234 basic " + basic + " token fake-url-token-1234"
	if out := r.String(text); out != "pass [redacted] basic [redacted] token [redacted]" {
		t.Errorf("redacted %q", out)
	}
}

// Reading and restoring a config file takes time in proportion to its size: 60,000 client keys,
// about the 1 MiB body limit, each take well under the bound, in JSON and in a config.yaml.
func TestRedactAndRestoreManySecrets(t *testing.T) {
	for _, isYAML := range []bool{false, true} {
		var b strings.Builder
		if isYAML {
			b.WriteString("access:\n  api-keys:\n")
			for i := range 60000 {
				fmt.Fprintf(&b, "    - \"sk-many-%06d\"\n", i)
			}
		} else {
			b.WriteString(`{"listen": "127.0.0.1:8317", "client_keys": [`)
			for i := range 60000 {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, `"sk-many-%06d"`, i)
			}
			b.WriteString("]}\n")
		}
		src := b.String()
		bound := 5 * time.Second
		if raceDetector {
			bound = 30 * time.Second
		}
		r := NewRedactor()
		start := time.Now()
		red, err := r.Redact([]byte(src), isYAML)
		if took := time.Since(start); err != nil || took > bound {
			t.Fatalf("yaml %v: Redact took %v (bound %v): %v", isYAML, took, bound, err)
		}
		start = time.Now()
		back, err := r.Restore(red, []byte(src), isYAML)
		if took := time.Since(start); err != nil || took > bound || string(back) != src {
			t.Fatalf("yaml %v: Restore took %v (bound %v), same document %v: %v", isYAML, took, bound, string(back) == src, err)
		}
	}
}

// Below the v8 api-keys mapping, only the provider sections core reads are read by the rules;
// any other key's values are secrets, as below any secret key.
func TestRedactUnknownV8Section(t *testing.T) {
	src := "api-keys:\n  codex:\n    - base-url: https://codex.example.com\n      keys:\n        - api-key: fake-codex-key-1\n  clients: [fake-misplaced-client-key-1]\n"
	redactRoundTrip(t, src, true, []string{"fake-codex-key-1", "fake-misplaced-client-key-1"}, []string{"base-url: https://codex.example.com"})
}

// Names are compared without regard to letter case and to how words are joined (camelCase,
// snake_case, kebab-case), and a mapping named like headers is read by the header rules.
func TestSecretNames(t *testing.T) {
	for _, name := range []string{
		"api_key", "apiKey", "API-KEY", "accessToken", "access_token", "refreshToken", "id-token",
		"client_secret", "clientSecret", "aws_secret_access_key", "AWS_SECRET_ACCESS_KEY", "secretAccessKey",
		"password", "dbPassword", "private_key", "privateKey", "secret_key", "management_key", "client_keys",
		"passphrase", "token", "secret",
	} {
		if !secretName(name) {
			t.Errorf("%q is not a secret name", name)
		}
	}
	for _, name := range []string{
		"max_tokens", "maxTokens", "max-output-tokens", "token_count_estimate", "prompt_cache_key",
		"cache-key", "partition_key", "key_id", "keys", "secretary", "base_url", "model", "kind",
		"passthrough-headers", "tokenizer", "monkey",
	} {
		if secretName(name) {
			t.Errorf("%q is a secret name", name)
		}
	}
	src := `{"settings": {"extra_headers": {"Authorization": "Bearer fake-extra-header-1", "X-Title": "kept-title"},
  "customHeaders": {"X-Api-Key": "fake-custom-header-1"}}}`
	got := SettingsSecrets([]byte(src))
	for _, want := range []string{"Bearer fake-extra-header-1", "fake-custom-header-1"} {
		if !slices.Contains(got, want) {
			t.Errorf("SettingsSecrets lacks %q: %q", want, got)
		}
	}
	if slices.Contains(got, "kept-title") {
		t.Errorf("SettingsSecrets holds a header that is not about authentication: %q", got)
	}
}
