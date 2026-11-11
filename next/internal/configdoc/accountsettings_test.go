package configdoc

import (
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
