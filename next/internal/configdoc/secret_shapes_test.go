package configdoc

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/secretshapes"
	"github.com/stdotwork/pluxy/next/protocol"
)

// corePieces returns the pieces of settings the way core takes them (internal/host's
// accountPieces).
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

// Core's path for the settings and URL shapes holds to the same hand-written expected values as
// the extension side's: SettingsSecrets, then AccountSecretsWith, then RemovePieces. Core also
// takes a URL's user info as written, so a value with escapes in it can lose a shorter cut than
// the limit: the check of the cut just below the limit is for a value written as it is.
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
					// Core also counts a URL's "user:password" together, so a password under 8
					// bytes is covered through that form where the extension side leaves it.
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

// A URL's secret parts on core's path: in error text, in the whole-value redaction of everything a
// call sends back, and as a config view shows them.
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

// A number below a secret key is found by core's rules and by the extension side's, with the same
// expected text.
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

// The config views: a URL with a secret parameter in its fragment shows as a placeholder and goes
// back unchanged, in settings and in a config file. A URL whose fragment holds none still shows.
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
