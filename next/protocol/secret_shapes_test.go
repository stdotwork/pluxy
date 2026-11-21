package protocol

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/secretshapes"
)

// shapeFields are the fields of an account and a login, each with its kind and how a value gets
// there.
func shapeFields() []struct {
	name, kind string
	secrets    func(v string) []string
} {
	type field = struct {
		name, kind string
		secrets    func(v string) []string
	}
	cred := func(c Credential) []string { return AccountSecrets(Account{Credential: &c}) }
	return []field{
		{"api_key", secretshapes.InCredential, func(v string) []string { return cred(Credential{APIKey: v}) }},
		{"access_token", secretshapes.InCredential, func(v string) []string { return cred(Credential{AccessToken: v}) }},
		{"refresh_token", secretshapes.InCredential, func(v string) []string { return cred(Credential{RefreshToken: v}) }},
		// internal/host/credentials.go passes a login's secret input that is no JSON object so.
		{"login secret input", secretshapes.InCredential, func(v string) []string { return cred(Credential{APIKey: v}) }},
		{"secret header", secretshapes.InHeader, func(v string) []string {
			return AccountSecrets(Account{Headers: map[string]string{"X-Api-Key": v}})
		}},
		{"Cookie header", secretshapes.InCookie, func(v string) []string {
			return AccountSecrets(Account{Headers: map[string]string{"Cookie": v}})
		}},
		{"cookie header, lower case", secretshapes.InCookie, func(v string) []string {
			return AccountSecrets(Account{Headers: map[string]string{"cookie": v}})
		}},
		{"settings secret", secretshapes.InSettings, func(v string) []string {
			b, _ := json.Marshal(map[string]string{"api_key": v})
			return AccountSecrets(Account{Settings: b})
		}},
		{"settings secret, core's rules", secretshapes.InSettings, func(v string) []string {
			return AccountSecretsWith(Account{}, []string{v})
		}},
		{"proxy user", secretshapes.InUserinfo, func(v string) []string {
			u := url.URL{Scheme: "http", User: url.UserPassword(v, "x"), Host: "proxy.example:3128"}
			return AccountSecrets(Account{ProxyURL: u.String()})
		}},
		{"proxy password", secretshapes.InUserinfo, func(v string) []string {
			u := url.URL{Scheme: "http", User: url.UserPassword("x", v), Host: "proxy.example:3128"}
			return AccountSecrets(Account{ProxyURL: u.String()})
		}},
		{"settings URL password", secretshapes.InUserinfo, func(v string) []string {
			u := url.URL{Scheme: "https", User: url.UserPassword("x", v), Host: "gw.example", Path: "/v1"}
			b, _ := json.Marshal(map[string]string{"base_url": u.String()})
			return AccountSecrets(Account{Settings: b})
		}},
		{"settings URL query", secretshapes.InQuery, func(v string) []string {
			b, _ := json.Marshal(map[string]string{"base_url": "https://gw.example/v1?token=" + url.QueryEscape(v)})
			return AccountSecrets(Account{Settings: b})
		}},
		{"settings URL fragment", secretshapes.InQuery, func(v string) []string {
			b, _ := json.Marshal(map[string]string{"base_url": "https://gw.example/cb#access_token=" + url.QueryEscape(v)})
			return AccountSecrets(Account{Settings: b})
		}},
		{"settings URL query after ;", secretshapes.InQuery, func(v string) []string {
			b, _ := json.Marshal(map[string]string{"base_url": "https://gw.example/v1?model=m;token=" + url.QueryEscape(v)})
			return AccountSecrets(Account{Settings: b})
		}},
		// internal/host/credentials.go passes a login's input that is a URL so.
		{"login URL password", secretshapes.InUserinfo, func(v string) []string {
			u := url.URL{Scheme: "https", User: url.UserPassword("x", v), Host: "gw.example", Path: "/cb"}
			return AccountSecretsWith(Account{}, []string{u.String()})
		}},
		{"login URL query", secretshapes.InQuery, func(v string) []string {
			return AccountSecretsWith(Account{}, []string{"https://gw.example/cb?code=" + url.QueryEscape(v)})
		}},
		{"login URL fragment", secretshapes.InQuery, func(v string) []string {
			return AccountSecretsWith(Account{}, []string{"https://gw.example/cb#code=" + url.QueryEscape(v)})
		}},
	}
}

// Every shape against every kind of secret field: what is real secret leaves an error from the cut
// length that applies, a cut just below it stays, and so do the public words. The expected values
// are written by hand in secretshapes.Rows.
func TestSecretShapes(t *testing.T) {
	removed := func(text string, secrets []string) bool {
		return RemovePieces("upstream said "+text+" here", secrets...) == "upstream said "+Redacted+" here"
	}
	kept := func(text string, secrets []string) bool {
		in := "upstream said " + text + " here"
		return RemovePieces(in, secrets...) == in
	}
	for _, row := range secretshapes.Rows() {
		for _, f := range shapeFields() {
			t.Run(row.Name+"/"+f.name, func(t *testing.T) {
				want, ok := row.Want[f.kind]
				if !ok {
					if row.Cannot[f.kind] == "" {
						t.Fatalf("the table says nothing of %s", f.kind)
					}
					t.Skip(row.Cannot[f.kind])
				}
				secrets := f.secrets(row.Value)
				for _, limit := range []struct {
					runs []string
					n    int
				}{{want.At4, minPiece}, {want.At8, longPiece}} {
					for _, run := range limit.runs {
						for _, cut := range secretshapes.Cuts(run, limit.n) {
							if !removed(cut, secrets) {
								t.Errorf("%d-byte cut %q of %q stayed in an error; secrets %q", len(cut), cut, row.Value, secrets)
							}
						}
						if below := run[:limit.n-1]; !want.Written && !kept(below, secrets) {
							t.Errorf("%q, under the %d-byte limit, was removed; secrets %q", below, limit.n, secrets)
						}
					}
				}
				for _, word := range want.Stay {
					if !kept(word, secrets) {
						t.Errorf("public word %q was removed; secrets %q", word, secrets)
					}
				}
				for _, s := range secrets {
					if strings.Trim(s, "=") == "" && strings.Trim(row.Value, "=") != "" {
						t.Errorf("padding %q counts as a secret", s)
					}
				}
			})
		}
	}
}

// The Redactor removes a credential's whole value and each part an upstream may echo alone, in
// every credential field, and keeps the public words.
func TestSecretShapesRedactor(t *testing.T) {
	for _, row := range secretshapes.Rows() {
		want, ok := row.Want[secretshapes.InCredential]
		if !ok || row.NoRedactor != "" {
			continue
		}
		for name, c := range map[string]*Credential{
			"api_key":       {Kind: CredentialAPIKey, APIKey: row.Value},
			"access_token":  {Kind: CredentialOAuth, AccessToken: row.Value},
			"refresh_token": {Kind: CredentialOAuth, RefreshToken: row.Value},
		} {
			t.Run(row.Name+"/"+name, func(t *testing.T) {
				r := NewRedactor(c)
				if got := r.String("said " + row.Value + " here"); strings.Contains(got, row.Value) {
					t.Errorf("the whole value stayed: %q", got)
				}
				for _, form := range row.Forms {
					if got, wanted := r.String("said "+form+" here"), "said "+Redacted+" here"; got != wanted {
						t.Errorf("part %q: String = %q, want %q", form, got, wanted)
					}
				}
				for _, word := range want.Stay {
					in := "said " + word + " here"
					if got := r.String(in); got != in {
						t.Errorf("public word %q: String = %q", word, got)
					}
				}
			})
		}
	}
}

// Which words of a URL count: its user name, password and the values of its parameters named like
// a secret, in the query or the fragment an OAuth redirect carries, split on "&" or ";". The host,
// the scheme and other parameters stay.
func TestSecretShapesURLParts(t *testing.T) {
	for _, c := range secretshapes.URLRows() {
		for name, secrets := range map[string][]string{
			"settings":  AccountSecrets(Account{Settings: mustJSON(t, map[string]string{"base_url": c.URL})}),
			"login URL": AccountSecretsWith(Account{}, []string{c.URL}),
		} {
			t.Run(c.Name+"/"+name, func(t *testing.T) {
				for _, run := range c.Gone {
					in := "upstream said " + run[:minPiece] + " here"
					if got := RemovePieces(in, secrets...); got != "upstream said "+Redacted+" here" {
						t.Errorf("cut %q stayed: %q; secrets %q", run[:minPiece], got, secrets)
					}
				}
				for _, word := range c.Stay {
					in := "upstream said " + word + " here"
					if got := RemovePieces(in, secrets...); got != in {
						t.Errorf("word %q: %q; secrets %q", word, got, secrets)
					}
				}
			})
		}
	}
}

// A number below a secret settings key counts with its exact digits, from 8 bytes, as core's rules
// for settings have it.
func TestSecretShapesNumbers(t *testing.T) {
	for _, c := range secretshapes.NumberRows() {
		t.Run(c.Name, func(t *testing.T) {
			secrets := AccountSecrets(Account{Settings: []byte(c.Settings)})
			for _, cut := range c.Gone {
				in := "upstream said " + cut + " here"
				if got := RemovePieces(in, secrets...); got != "upstream said "+Redacted+" here" {
					t.Errorf("cut %q of %s stayed: %q; secrets %q", cut, c.Settings, got, secrets)
				}
			}
			for _, word := range c.Stay {
				in := "upstream said " + word + " here"
				if got := RemovePieces(in, secrets...); got != in {
					t.Errorf("%q of %s: %q; secrets %q", word, c.Settings, got, secrets)
				}
			}
		})
	}
}

// Settings that are not one JSON value hold no secret AccountSecrets can read.
func TestAccountSecretsSettingsNotJSON(t *testing.T) {
	for _, settings := range []string{`{"api_key":"Qx7Rv2Lm9Tz4"} trailing`, `{"api_key":`} {
		if got := AccountSecrets(Account{Settings: []byte(settings)}); len(got) != 0 {
			t.Errorf("AccountSecrets(%s) = %q, want nothing", settings, got)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A run with characters outside the token characters is read whole only for a secret that holds
// such characters: for any other, the error's punctuation still ends a run.
func TestRemovePiecesPunctuationOnlyForSecretsWithIt(t *testing.T) {
	in := "bad key (" + secretshapes.Token[:4] + ") and more!"
	if got, want := RemovePieces(in, secretshapes.Token), "bad key ([redacted]) and more!"; got != want {
		t.Errorf("RemovePieces = %q, want %q", got, want)
	}
	// Quotes and commas end a run even for a secret that holds them, so a JSON error keeps its
	// structure and loses only the word that shares a piece.
	const body = `{"error":"bad","user":"bob"}`
	if got, want := RemovePieces(body, `{"api_key":"Ab3Cd5Ef","user":"svc"}`), `{"error":"bad","[redacted]":"bob"}`; got != want {
		t.Errorf("RemovePieces(%q) = %q, want %q", body, got, want)
	}
	// A secret that holds ":" joins a URL's parts into one run. The whole URL goes only when that
	// run holds a piece of a secret; a URL that holds none stays.
	const colon = "Fk3Rv2:" + secretshapes.Token
	for in, want := range map[string]string{
		"dial https://gw.example:8443/Qx7Rv2 failed": "dial [redacted] failed",
		"dial https://gw.example:8443/v1 failed":     "dial https://gw.example:8443/v1 failed",
	} {
		if got := RemovePieces(in, colon); got != want {
			t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
		}
	}
	const pw = "P@ss!w0rd#Fake1"
	for in, want := range map[string]string{
		"cut P@ss!w here":        "cut [redacted] here",
		"cut ss!w here":          "cut [redacted] here",
		"cut P@s here":           "cut P@s here",
		"[redacted] stays!":      "[redacted] stays!",
		"nothing of it, friend!": "nothing of it, friend!",
	} {
		if got := RemovePieces(in, pw); got != want {
			t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
		}
	}
}
