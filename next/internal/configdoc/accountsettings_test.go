package configdoc

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
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
	if got := d.TextSecrets(); !slices.Equal(got, []string{"fake-loop"}) {
		t.Errorf("TextSecrets() of a self-referencing anchor = %q", got)
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
	if got := d.TextSecrets(); !slices.Equal(got, []string{"fake-deep"}) {
		t.Errorf("TextSecrets() of nested anchors = %q", got)
	}
}

// An alias below a secret key that leads back into itself, or a chain of anchors that each use
// the one before twice, is read once: TextSecrets returns, and quickly.
func TestSecretsOfAliasLoopsAndChains(t *testing.T) {
	var chain strings.Builder
	chain.WriteString("a0: &a0 [fake-chain]\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&chain, "a%d: &a%d [*a%d, *a%d]\n", i, i, i-1, i-1)
	}
	chain.WriteString("password: *a40\n")
	for name, src := range map[string]string{
		"loop":  "x: &a [*a, fake-loop]\npassword: *a\n",
		"chain": chain.String(),
	} {
		d, err := Parse([]byte(src), true)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		done := make(chan []string, 1)
		go func() { done <- d.TextSecrets() }()
		select {
		case got := <-done:
			if len(got) != 1 {
				t.Errorf("%s: TextSecrets() = %q, want the one value once", name, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: TextSecrets() did not return in 2s", name)
		}
	}
}

// An alias stands for its anchor wherever a rule looks at a value: an accounts list given by
// alias, an account merged from a template with <<, and headers given by alias, in account
// settings and in a provider entry.
func TestRedactAliasesWhereRulesLookAtTheValue(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		secrets   []string
	}{
		{"accounts list", `list: &l
  - id: a
    settings:
      password: {nested: fake-list-nested}
extension:
  manifest: x.json
  accounts: *l
`, []string{"fake-list-nested"}},
		{"merged account", `base: &b
  settings:
    password: {nested: fake-merge-nested}
extension:
  manifest: x.json
  accounts:
    - <<: *b
      id: a
`, []string{"fake-merge-nested"}},
		{"headers", `h: &h
  Authorization: Bearer fake-header-alias
h2: &h2
  X-Api-Key: fake-settings-header
openai-compatibility:
  - name: local
    base-url: https://compat.example.com/v1
    headers: *h
extension:
  manifest: x.json
  accounts:
    - id: a
      settings:
        headers: *h2
`, []string{"Bearer fake-header-alias", "fake-settings-header"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, back := redactChecked(t, tc.src, true, tc.secrets); back != tc.src {
				t.Errorf("round trip is not byte exact:\n%s", back)
			}
		})
	}
}

// Outside account settings, everything below a key named like a secret is a secret too, written
// in place or given through an alias, except in the provider sections today's core reads at the
// top of a config.yaml: the v8 api-keys mapping and the older lists such as codex-api-key, whose
// entries are read by the rules. An extension's own "settings" in its entry under "extensions",
// which core does not read, is no exception.
func TestRedactBelowSecretKeysOutsideSettings(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		isYAML    bool
		secrets   []string
		kept      []string // text the redacted document still holds
	}{
		{"extension-wide settings", `extensions:
  pluxy/x:
    settings:
      password:
        nested: fake-ext-nested
`, true, []string{"fake-ext-nested"}, nil},
		{"extension-wide settings, json", `{"extensions": {"pluxy/x": {"settings": {"password": {"nested": "fake-ext-json"}}}}}`,
			false, []string{"fake-ext-json"}, nil},
		{"mapping", `management:
  secret-key:
    primary: fake-inline-map
`, true, []string{"fake-inline-map"}, nil},
		{"mapping through an alias", `template: &t
  nested: fake-aliased-map
routing:
  token: *t
`, true, []string{"fake-aliased-map"}, nil},
		{"list of mappings", `password:
  - user: fake-list-map
`, true, []string{"fake-list-map"}, nil},
		{"list of mappings through an alias", `template: &l
  - user: fake-aliased-list
server:
  private-key: *l
`, true, []string{"fake-aliased-list"}, nil},
		{"api-keys below the top", `access:
  api-keys:
    primary: fake-nested-api-keys
`, true, []string{"fake-nested-api-keys"}, nil},
		{"provider list below the top", `extensions:
  pluxy/x:
    codex-api-key:
      - base-url: fake-not-a-section
`, true, []string{"fake-not-a-section"}, nil},
		{"secret mapping in a provider entry", `codex-api-key:
  - api-key: fake-entry-key
    base-url: https://codex.example.com
    private-key:
      inner: fake-entry-inner
`, true, []string{"fake-entry-key", "fake-entry-inner"}, []string{"base-url: https://codex.example.com"}},
		{"provider sections through an alias", `groups: &g
  - api-key: fake-aliased-section
    base-url: https://codex.example.com
codex-api-key: *g
sections: &s
  claude:
    - keys:
        - api-key: fake-aliased-v8
      base-url: https://claude.example.com
api-keys: *s
`, true, []string{"fake-aliased-section", "fake-aliased-v8"},
			[]string{"base-url: https://codex.example.com", "base-url: https://claude.example.com"}},
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
