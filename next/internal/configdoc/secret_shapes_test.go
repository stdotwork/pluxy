package configdoc

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/secretshapes"
	"github.com/stdotwork/pluxy/next/protocol"
)

func corePieces(settings []byte) []string {
	return protocol.AccountSecretsWith(protocol.Account{}, SettingsSecrets(settings))
}

func settingsOf(t *testing.T, key, value string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func goneFromError(text string, secrets []string) bool {
	return protocol.RemovePieces("upstream said "+text+" here", secrets...) == "upstream said "+protocol.Redacted+" here"
}

func keptInError(text string, secrets []string) bool {
	in := "upstream said " + text + " here"
	return protocol.RemovePieces(in, secrets...) == in
}

func TestSecretShapesCoreSettings(t *testing.T) {
	type field struct {
		name, kind string
		settings   func(v string) []byte
	}
	fields := []field{
		{"settings secret", secretshapes.InSettings, func(v string) []byte { return settingsOf(t, "api_key", v) }},
		{"settings URL password", secretshapes.InUserinfo, func(v string) []byte {
			u := url.URL{Scheme: "https", User: url.UserPassword("x", v), Host: "gw.example", Path: "/v1"}
			return settingsOf(t, "base_url", u.String())
		}},
		{"settings URL query", secretshapes.InQuery, func(v string) []byte {
			return settingsOf(t, "base_url", "https://gw.example/v1?token="+url.QueryEscape(v))
		}},
		{"settings URL query after ;", secretshapes.InQuery, func(v string) []byte {
			return settingsOf(t, "base_url", "https://gw.example/v1?model=m;token="+url.QueryEscape(v))
		}},
		{"settings URL fragment", secretshapes.InQuery, func(v string) []byte {
			return settingsOf(t, "base_url", "https://gw.example/cb#access_token="+url.QueryEscape(v))
		}},
	}
	for _, row := range secretshapes.Rows() {
		for _, f := range fields {
			t.Run(row.Name+"/"+f.name, func(t *testing.T) {
				want, ok := row.Want[f.kind]
				if !ok {
					t.Skip(row.Cannot[f.kind])
				}
				secrets := corePieces(f.settings(row.Value))
				asWritten := f.kind == secretshapes.InSettings || url.QueryEscape(row.Value) == row.Value
				for _, limit := range []struct {
					runs []string
					n    int
				}{{want.At4, 4}, {want.At8, 8}} {
					for _, run := range limit.runs {
						for _, cut := range secretshapes.Cuts(run, limit.n) {
							if !goneFromError(cut, secrets) {
								t.Errorf("%d-byte cut %q of %q stayed in an error; secrets %q", len(cut), cut, row.Value, secrets)
							}
						}
						if below := run[:limit.n-1]; asWritten && !want.Written && !keptInError(below, secrets) {
							t.Errorf("%q, under the %d-byte limit, was removed; secrets %q", below, limit.n, secrets)
						}
					}
				}
				for _, word := range want.Stay {
					if f.kind == secretshapes.InUserinfo && word == row.Value && len(row.Value) < MinTextSecret {
						continue
					}
					if !keptInError(word, secrets) {
						t.Errorf("public word %q was removed; secrets %q", word, secrets)
					}
				}
			})
		}
	}
}

func TestSecretShapesCoreURLParts(t *testing.T) {
	for _, c := range secretshapes.URLRows() {
		t.Run(c.Name, func(t *testing.T) {
			settings := settingsOf(t, "base_url", c.URL)
			secrets := corePieces(settings)
			for _, run := range c.Gone {
				if !goneFromError(run[:4], secrets) {
					t.Errorf("cut %q stayed in an error; secrets %q", run[:4], secrets)
				}
			}
			for _, word := range c.Stay {
				if !keptInError(word, secrets) {
					t.Errorf("word %q was removed; secrets %q", word, secrets)
				}
			}
			data, _ := json.Marshal(SettingsSecrets(settings))
			r := protocol.NewRedactor(&protocol.Credential{Kind: "settings", Data: data})
			for _, value := range c.Whole {
				if got, want := r.String("said "+value+" here"), "said "+protocol.Redacted+" here"; got != want {
					t.Errorf("Redactor: String(%q) = %q, want %q", value, got, want)
				}
			}
		})
	}
}

func TestSecretShapesCoreNumbers(t *testing.T) {
	for _, c := range secretshapes.NumberRows() {
		t.Run(c.Name, func(t *testing.T) {
			for name, secrets := range map[string][]string{
				"core":      corePieces([]byte(c.Settings)),
				"extension": protocol.AccountSecrets(protocol.Account{Settings: []byte(c.Settings)}),
			} {
				for _, cut := range c.Gone {
					if !goneFromError(cut, secrets) {
						t.Errorf("%s: cut %q of %s stayed in an error; secrets %q", name, cut, c.Settings, secrets)
					}
				}
				for _, word := range c.Stay {
					if !keptInError(word, secrets) {
						t.Errorf("%s: %q of %s was removed; secrets %q", name, word, c.Settings, secrets)
					}
				}
			}
		})
	}
}

func TestSecretFragmentInConfigViews(t *testing.T) {
	const secretURL = "https://gw.example/cb#access_token=" + secretshapes.Token
	const plainURL = "https://gw.example/docs#section"
	doc := []byte(`{"base_url":"` + secretURL + `","docs_url":"` + plainURL + `"}`)
	r := NewRedactor()
	for name, view := range map[string]struct {
		redact  func([]byte) ([]byte, error)
		restore func(edited, current []byte) ([]byte, error)
	}{
		"settings": {r.RedactSettings, r.RestoreSettings},
		"config": {
			func(b []byte) ([]byte, error) { return r.Redact(b, false) },
			func(edited, current []byte) ([]byte, error) { return r.Restore(edited, current, false) },
		},
	} {
		shown, err := view.redact(doc)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(shown), secretshapes.Token) {
			t.Errorf("%s view shows the secret: %s", name, shown)
		}
		if !strings.Contains(string(shown), plainURL) {
			t.Errorf("%s view hides a URL with no secret: %s", name, shown)
		}
		back, err := view.restore(shown, doc)
		if err != nil {
			t.Fatalf("%s restore: %v", name, err)
		}
		if string(back) != string(doc) {
			t.Errorf("%s restore = %s, want %s", name, back, doc)
		}
	}
}
