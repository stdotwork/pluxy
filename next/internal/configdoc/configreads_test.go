package configdoc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
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

// Text keeps a number shorter than MinTextSecret that is a secret only for being in a mapping
// below a secret key (the 3 of rotate_after_days: 3), and loses every other secret, however
// short: a string there (hunter2), a secret key's value, its list's items, and a value that is
// also one of those through an alias.
func TestTextSecretsLeaveShortNestedNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		isYAML    bool
		gone      []string
	}{
		{"json", `{"management_key": "mk", "client_keys": ["ck", ["ck-deep"]],
  "extension": {"accounts": [{"id": "a", "settings": {"client_secret": {"value": "fake-nested-secret-1", "rotate_after_days": 3, "ratio": 1.5, "mode": "auto"},
    "password": {"value": "hunter2"}}}]}}`,
			false, []string{"mk", "ck", "ck-deep", "fake-nested-secret-1", "auto", "hunter2"}},
		{"yaml", "request-retry: 3\nbedrock-api-key:\n  - api-key: fake-bedrock-key-1\n    priority: 3\n    ratio: 1.5\n    mode: auto\n    region: &r eu-1\nremote-management:\n  secret-key: *r\n",
			true, []string{"fake-bedrock-key-1", "auto", "eu-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mustParse(t, tc.src, tc.isYAML)
			nodes, _ := d.secretNodes()
			for _, s := range []string{"3", "1.5"} {
				if !slices.ContainsFunc(nodes, func(n *yaml.Node) bool { return n.Value == s }) {
					t.Fatalf("%s is no secret of the document", s)
				}
			}
			r := NewRedactor()
			msg := "line 3: ratio 1.5"
			want := msg
			for _, s := range tc.gone {
				msg += ", " + s
				want += ", " + r.Placeholder(s)
			}
			if got := r.Scrub(msg, d.TextSecrets()); got != want {
				t.Errorf("Scrub(TextSecrets) = %q, want %q", got, want)
			}
		})
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
			if !slices.ContainsFunc(d.TextSecrets(), func(s string) bool { return strings.Contains(s, tc.secret) }) {
				t.Errorf("TextSecrets() = %q, want one holding %q", d.TextSecrets(), tc.secret)
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
// user name and password together, in Basic form too, and each value of 8 bytes or more of a
// query parameter named like a secret, as written and decoded. Other query values, such as a
// date or a region, are no secrets.
func TestSettingsSecretsHoldURLParts(t *testing.T) {
	const u = "https://gw-user:fake-gw-pass-1234@up.example/v1?token=fake-url-token-1234&v=2" +
		"&api-version=2024-10-21&region=us-east-1&key=sk+live/abc%3D%3D1234&sig=fake-url-sig-1234"
	got := SettingsSecrets([]byte(`{"base_url": "` + u + `"}`))
	for _, want := range []string{
		u,
		"fake-gw-pass-1234",
		"gw-user:fake-gw-pass-1234",
		"fake-url-token-1234",
		"sk+live/abc%3D%3D1234", // as written
		"sk live/abc==1234",     // as a query decodes it
		"sk+live/abc==1234",     // with only its escapes decoded
		"fake-url-sig-1234",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("SettingsSecrets lacks %q: %q", want, got)
		}
	}
	for _, plain := range []string{"2", "gw-user", "2024-10-21", "us-east-1"} {
		if slices.Contains(got, plain) {
			t.Errorf("SettingsSecrets holds %q, no secret: %q", plain, got)
		}
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

// Reading and restoring a config file, and finding the secrets of settings, take time in
// proportion to the document's size: 16 times the secrets take about 16 times as long, not the
// 256 times a reading that goes over the document once for each secret takes. A config.yaml is
// read with a key on each line and with all of them on one line (a flow list), with and without
// characters beyond ASCII. The best of three runs at each size is compared, so a busy machine
// slows both alike and decides nothing.
func TestRedactAndRestoreTimeGrowsWithSize(t *testing.T) {
	const small, large, most = 1000, 16000, 96.0
	type layout struct {
		name   string
		isYAML bool
		doc    func(n int) []byte
	}
	yamlList := func(flow bool, key string) func(n int) []byte {
		return func(n int) []byte {
			var b strings.Builder
			if flow {
				b.WriteString("access:\n  api-keys: [")
			} else {
				b.WriteString("access:\n  api-keys:\n")
			}
			for i := range n {
				switch {
				case !flow:
					fmt.Fprintf(&b, "    - \"%s-%06d\"\n", key, i)
				case i > 0:
					b.WriteString(", ")
					fallthrough
				default:
					fmt.Fprintf(&b, "\"%s-%06d\"", key, i)
				}
			}
			if flow {
				b.WriteString("]\n")
			}
			return []byte(b.String())
		}
	}
	layouts := []layout{
		{"json", false, func(n int) []byte {
			var b strings.Builder
			b.WriteString(`{"client_keys": [`)
			for i := range n {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, `"sk-many-%06d"`, i)
			}
			b.WriteString("]}\n")
			return []byte(b.String())
		}},
		{"yaml", true, yamlList(false, "sk-many")},
		{"yaml on one line", true, yamlList(true, "sk-many")},
		{"yaml on one line, not ASCII", true, yamlList(true, "sk-mäny")},
	}
	// timed runs f with the garbage collector off, which would otherwise run more often the more
	// memory f takes, and make a larger document slower than its size alone makes it.
	timed := func(f func()) time.Duration {
		runtime.GC()
		defer debug.SetGCPercent(debug.SetGCPercent(-1))
		start := time.Now()
		f()
		return time.Since(start)
	}
	r := NewRedactor()
	for _, l := range layouts {
		steps := map[string]func(src []byte) func(){
			"Redact": func(src []byte) func() {
				return func() {
					if _, err := r.Redact(src, l.isYAML); err != nil {
						t.Fatal(err)
					}
				}
			},
			"Restore": func(src []byte) func() {
				red, err := r.Redact(src, l.isYAML)
				if err != nil {
					t.Fatal(err)
				}
				return func() {
					if back, err := r.Restore(red, src, l.isYAML); err != nil || !bytes.Equal(back, src) {
						t.Fatalf("Restore: %v", err)
					}
				}
			},
		}
		if !l.isYAML {
			steps["SettingsSecrets"] = func(src []byte) func() {
				return func() {
					if got := SettingsSecrets(src); len(got) == 0 {
						t.Fatal("SettingsSecrets found none")
					}
				}
			}
		}
		for name, step := range steps {
			few, many := step(l.doc(small)), step(l.doc(large))
			best := [2]time.Duration{time.Hour, time.Hour}
			for range 3 {
				best[0] = min(best[0], timed(few))
				best[1] = min(best[1], timed(many))
			}
			if ratio := float64(best[1]) / float64(best[0]); ratio > most {
				t.Errorf("%s: %s of %d secrets took %v, of %d took %v: %.1f times as long, want at most %.0f", l.name, name, small, best[0], large, best[1], ratio, most)
			} else {
				t.Logf("%s: %s: %v, %v: %.1f times", l.name, name, best[0], best[1], ratio)
			}
		}
	}
}

// A secret's value written again as a whole value where no secret goes (a note holding the key)
// is hidden there too, and goes back there; a copy shorter than MinTextSecret, or a number, is
// no secret and stays shown, as for F1's 3. A copy's placeholder goes back only where the file
// holds that copy now.
func TestRedactCopiesOfSecrets(t *testing.T) {
	const key = "sk-real-secret-value-1234"
	for _, tc := range []struct {
		name, src string
		isYAML    bool
		shown     []string
		moved     string // the redacted document with the copy's placeholder moved
	}{
		{"yaml", "api-key: " + key + "\nnote: " + key + "\nnotes: [first, \"" + key + "\"]\nsecret: abcd\nlabel: abcd\nretry: 12345678\nprivate-key: \"12345678\"\n",
			true, []string{"label: abcd", "retry: 12345678"}, "other: P\n"},
		{"json", `{"api_key": "` + key + `", "note": "` + key + `", "notes": ["first", "` + key + `"], "secret": "abcd", "label": "abcd"}` + "\n",
			false, []string{`"label": "abcd"`}, `{"other": "P"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			redactRoundTrip(t, tc.src, tc.isYAML, []string{key}, tc.shown)
			r := NewRedactor()
			moved := strings.ReplaceAll(tc.moved, "P", r.Placeholder(key))
			if _, err := r.Restore([]byte(moved), []byte(tc.src), tc.isYAML); !errors.Is(err, ErrInvalid) {
				t.Errorf("Restore of a copy's placeholder moved to another key: %v, want ErrInvalid", err)
			}
		})
	}
	// In settings the same.
	r := NewRedactor()
	src := `{"api_key": "` + key + `", "note": "` + key + `"}`
	red, err := r.RedactSettings([]byte(src))
	if err != nil || strings.Contains(string(red), key) {
		t.Fatalf("RedactSettings: %v\n%s", err, red)
	}
	if back, err := r.RestoreSettings(red, []byte(src)); err != nil || string(back) != src {
		t.Errorf("RestoreSettings: %v\n%s", err, back)
	}
	// A placeholder that stands for no secret, where no secret goes, is stale.
	stale := `{"api_key": "` + key + `", "note": "` + r.Placeholder("no-such-secret-1234") + `"}`
	if _, err := r.RestoreSettings([]byte(stale), []byte(src)); !errors.Is(err, ErrStale) {
		t.Errorf("RestoreSettings of an unknown placeholder on a plain key: %v, want ErrStale", err)
	}
}

// A copy's placeholder goes back only while the document still holds the secret it copies as
// that secret: one whose original was changed or deleted is refused, naming the copy's line, so
// the copy is never put back where it would be shown.
func TestRestoreCopyNeedsItsOriginal(t *testing.T) {
	const key = "sk-real-secret-value-1234"
	for _, tc := range []struct {
		name, src, original, changed string
		isYAML, settings             bool
	}{
		{"yaml", "api-key: " + key + "\nnote: " + key + "\n", "api-key: P\n", "api-key: brand-new-key-999999\n", true, false},
		{"json", "{\n  \"api_key\": \"" + key + "\",\n  \"note\": \"" + key + "\"\n}\n", "  \"api_key\": \"P\",\n", "  \"api_key\": \"brand-new-key-999999\",\n", false, false},
		{"settings", "{\n  \"api_key\": \"" + key + "\",\n  \"note\": \"" + key + "\"\n}\n", "  \"api_key\": \"P\",\n", "  \"api_key\": \"brand-new-key-999999\",\n", false, true},
	} {
		r := NewRedactor()
		redact, restore := func(b []byte) ([]byte, error) { return r.Redact(b, tc.isYAML) }, func(e, c []byte) ([]byte, error) { return r.Restore(e, c, tc.isYAML) }
		if tc.settings {
			redact, restore = r.RedactSettings, r.RestoreSettings
		}
		red, err := redact([]byte(tc.src))
		if err != nil {
			t.Fatal(err)
		}
		original := strings.ReplaceAll(tc.original, "P", r.Placeholder(key))
		for what, with := range map[string]string{"changed": tc.changed, "deleted": ""} {
			t.Run(tc.name+"/"+what, func(t *testing.T) {
				edited := strings.Replace(string(red), original, with, 1)
				if edited == string(red) {
					t.Fatalf("the test's edit did not apply to:\n%s", red)
				}
				line := 1 + strings.Count(edited[:strings.Index(edited, "note")], "\n")
				back, err := restore([]byte(edited), []byte(tc.src))
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), fmt.Sprintf("line %d:", line)) || !strings.Contains(err.Error(), "write the value itself") {
					t.Errorf("Restore = %v\n%s\nwant ErrInvalid naming line %d and saying to write the value itself", err, back, line)
				}
			})
		}
	}
}

// After core restarts, a placeholder of the old key stands for no secret: it is stale, whether
// a copy comes before its original or after it.
func TestRestoreAfterRestartIsStale(t *testing.T) {
	const key = "sk-real-secret-value-1234"
	for _, tc := range []struct {
		name, src        string
		isYAML, settings bool
	}{
		{"yaml copy first", "note: " + key + "\napi-key: " + key + "\n", true, false},
		{"yaml copy after", "api-key: " + key + "\nnote: " + key + "\n", true, false},
		{"json copy first", `{"note": "` + key + `", "api_key": "` + key + `"}`, false, false},
		{"json copy after", `{"api_key": "` + key + `", "note": "` + key + `"}`, false, false},
		{"settings copy first", `{"note": "` + key + `", "api_key": "` + key + `"}`, false, true},
		{"settings copy after", `{"api_key": "` + key + `", "note": "` + key + `"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := NewRedactor(), NewRedactor()
			var red []byte
			var err error
			if tc.settings {
				red, err = before.RedactSettings([]byte(tc.src))
			} else {
				red, err = before.Redact([]byte(tc.src), tc.isYAML)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.settings {
				_, err = after.RestoreSettings(red, []byte(tc.src))
			} else {
				_, err = after.Restore(red, []byte(tc.src), tc.isYAML)
			}
			if !errors.Is(err, ErrStale) {
				t.Errorf("Restore after a restart: %v, want ErrStale", err)
			}
		})
	}
}

// Only a secret by itself (a secret key's value, a credential's, a secret header's) is copied:
// a value that is a secret only for being in a mapping below a secret key (the mode of
// client-secret: {mode: automatic}) leaves the same value elsewhere shown.
func TestRedactCopiesOnlyOfDirectSecrets(t *testing.T) {
	redactRoundTrip(t, "routing:\n  strategy: automatic\nclient-secret:\n  mode: automatic\n  value: fake-client-secret-1\n", true,
		[]string{"fake-client-secret-1", "mode: automatic"}, []string{"strategy: automatic"})
	redactRoundTrip(t, `{"routing": {"strategy": "automatic"}, "client_secret": {"mode": "automatic", "value": "fake-client-secret-1"}}`, false,
		[]string{"fake-client-secret-1", `"mode": "automatic"`}, []string{`"strategy": "automatic"`})
}

// A JSON key name is compared as encoding/json matches it when core loads the file, so
// client_keyſ (with U+017F) and management_\u212aey (with the Kelvin sign) are client_keys and
// management_key, and their values are hidden.
func TestRedactFoldedJSONNames(t *testing.T) {
	src := "{\"listen\": \"127.0.0.1:0\", \"client_key\u017f\": [\"fake-fold-client-key-1\"], \"management_\u212aey\": \"fake-fold-mk-1\"}\n"
	redactRoundTrip(t, src, false, []string{"fake-fold-client-key-1", "fake-fold-mk-1"}, nil)
}

// Below the v8 api-keys mapping, only the provider sections core reads are read by the rules;
// any other key's values are secrets, as below any secret key.
func TestRedactUnknownV8Section(t *testing.T) {
	src := "api-keys:\n  codex:\n    - base-url: https://codex.example.com\n      keys:\n        - api-key: fake-codex-key-1\n  clients: [fake-misplaced-client-key-1]\n"
	redactRoundTrip(t, src, true, []string{"fake-codex-key-1", "fake-misplaced-client-key-1"}, []string{"base-url: https://codex.example.com"})
}

// Names are compared without regard to letter case and to how words are joined, if they are
// (camelCase, snake_case, kebab-case, written together), and a mapping whose name holds header
// is read by the header rules, where a key named like a secret is one too.
func TestSecretNames(t *testing.T) {
	for _, name := range []string{
		"api_key", "apiKey", "API-KEY", "accessToken", "access_token", "refreshToken", "id-token",
		"client_secret", "clientSecret", "aws_secret_access_key", "AWS_SECRET_ACCESS_KEY", "secretAccessKey",
		"password", "dbPassword", "private_key", "privateKey", "secret_key", "management_key", "client_keys",
		"passphrase", "token", "secret", "accesstoken", "apitoken", "clientsecret", "secretkey", "privatekey",
		"awssecretaccesskey", "dbpassword", "x-apikey", "client_secrets", "secrets", "passwords", "dbPasswords",
		"api_key_value", "apiKey2", "privateKeyPem", "client_secret_b64", "TOKEN_VALUE",
	} {
		if !secretName(name) {
			t.Errorf("%q is not a secret name", name)
		}
	}
	for _, name := range []string{
		"max_tokens", "maxTokens", "max-output-tokens", "token_count_estimate", "prompt_cache_key",
		"cache-key", "partition_key", "key_id", "keys", "secretary", "base_url", "model", "kind",
		"passthrough-headers", "tokenizer", "monkey", "tokens", "api_key_id", "max_token_count", "secret_name",
		"token_url", "token_type", "private_key_id", "input_token_limit", "promptTokenCount", "value", "pem", "2",
	} {
		if secretName(name) {
			t.Errorf("%q is a secret name", name)
		}
	}
	src := `{"settings": {"extra_headers": {"Authorization": "Bearer fake-extra-header-1", "X-Title": "kept-title"},
  "customHeaders": {"X-Api-Key": "fake-custom-header-1"}, "upstreamheaders": {"Cookie": "fake-joined-header-1"},
  "extra_header": {"Authorization": "fake-singular-header-1"}, "headers_override": {"X-Auth": "fake-override-header-1"},
  "request_headers": {"passphrase": "fake-header-passphrase-1", "client_secret": "fake-header-secret-1"}}}`
	got := SettingsSecrets([]byte(src))
	for _, want := range []string{"Bearer fake-extra-header-1", "fake-custom-header-1", "fake-joined-header-1",
		"fake-singular-header-1", "fake-override-header-1", "fake-header-passphrase-1", "fake-header-secret-1"} {
		if !slices.Contains(got, want) {
			t.Errorf("SettingsSecrets lacks %q: %q", want, got)
		}
	}
	if slices.Contains(got, "kept-title") {
		t.Errorf("SettingsSecrets holds a header that is not about authentication: %q", got)
	}
}
