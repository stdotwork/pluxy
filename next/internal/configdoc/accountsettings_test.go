package configdoc

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// In the settings of a config file's accounts, everything below a key named like a secret is a
// secret, however deep, as in an account's settings on their own (RedactSettings), in a JSON
// config and in a config.yaml; a provider entry of today's core's layout keeps its plain values.
func TestRedactConfigAccountSettings(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		isYAML    bool
		secrets   []string
		kept      []string // text the redacted document still holds
	}{
		{"json", `{"client_keys": ["fake-client"],
  "extension": {"manifest": "x.json", "accounts": [{"id": "a",
    "settings": {"base_url": "https://one.example/v1", "password": {"primary": "fake-json-object"}, "api_key": [{"inner": "fake-json-in-list"}]},
    "credential": {"kind": "api_key", "api_key": "fake-json-credential"}}]},
  "extensions": {"pluxy/x": {"accounts": [{"id": "b", "settings": {"secret": {"deep": ["fake-json-deep"]}, "retries": 2}}]}}}`,
			false,
			[]string{"fake-client", "fake-json-object", "fake-json-in-list", "fake-json-credential", "fake-json-deep"},
			[]string{`"base_url": "https://one.example/v1"`, `"retries": 2`}},
		{"yaml", `api-keys:
  - fake-client
openai-compatibility:
  - name: local
    base-url: https://compat.example.com/v1
    api-key-entries:
      - api-key: fake-yaml-compat
extensions:
  pluxy/x:
    accounts:
      - id: b
        settings:
          base_url: https://one.example/v1
          password:
            primary: fake-yaml-object
          api_key:
            - inner: fake-yaml-in-list
`,
			true,
			[]string{"fake-client", "fake-yaml-compat", "fake-yaml-object", "fake-yaml-in-list"},
			[]string{"  - name: local\n    base-url: https://compat.example.com/v1\n", "base_url: https://one.example/v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			red, back := redactChecked(t, tc.src, tc.isYAML, tc.secrets)
			if back != tc.src {
				t.Errorf("round trip is not byte exact:\n%s", back)
			}
			for _, k := range tc.kept {
				if !strings.Contains(red, k) {
					t.Errorf("the redacted document lacks %q:\n%s", k, red)
				}
			}
		})
	}
}

// An account's settings, or the account, given through a YAML alias is read as strictly as when
// written in place, while the anchor's own place is read by its own rules; a self-referencing
// anchor does not stop the reading.
func TestRedactConfigAccountSettingsThroughAliases(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		secrets   []string
	}{
		{"settings", `settings-template: &s
  password:
    nested: fake-alias-nested
extensions:
  pluxy/x:
    accounts:
      - id: a
        settings: *s
`, []string{"fake-alias-nested"}},
		{"account", `account-template: &a
  id: a
  settings:
    api_key:
      - inner: fake-alias-account
extension:
  manifest: x.json
  accounts: [*a]
`, []string{"fake-alias-account"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, back := redactChecked(t, tc.src, true, tc.secrets); back != tc.src {
				t.Errorf("round trip is not byte exact:\n%s", back)
			}
		})
	}
	d, err := Parse([]byte("loop: &l\n  next: *l\n  accounts: [*l]\n  settings: {password: {nested: fake-loop}}\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Secrets(); !slices.Equal(got, []string{"fake-loop"}) {
		t.Errorf("Secrets() of a self-referencing anchor = %q", got)
	}
	// Anchors that each use the one before twice: following every use would read 2^40 nodes.
	var b strings.Builder
	b.WriteString("a0: &a0 {password: fake-deep}\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "a%d: &a%d {accounts: [{settings: *a%d}, {settings: *a%d}]}\n", i, i, i-1, i-1)
	}
	if d, err = Parse([]byte(b.String()), true); err != nil {
		t.Fatal(err)
	}
	if got := d.Secrets(); !slices.Equal(got, []string{"fake-deep"}) {
		t.Errorf("Secrets() of nested anchors = %q", got)
	}
}
