package protocol

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const partsJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJwYXJ0cy0xIn0.c2lnbmF0dXJlLW9mLXBhcnRzLXRlc3Q"

func TestSecretParts(t *testing.T) {
	basic := base64.StdEncoding.EncodeToString([]byte("gw-user:gw-password-123"))
	for _, c := range []struct {
		name, secret string
		want         []string
	}{
		{"devin session token", "devin-session-token$" + partsJWT, []string{partsJWT}},
		{"meta device token", "dca:meta-device-0123456789", []string{"meta-device-0123456789"}},
		{"bearer", "Bearer gw-token-0123456789", []string{"gw-token-0123456789"}},
		{"bearer in lower case, spaces", "bearer   gw-token-0123456789", []string{"gw-token-0123456789"}},
		{"basic", "Basic " + basic, []string{basic, "gw-user:gw-password-123", "gw-password-123"}},
		{"cookies", `sid=cookie-sid-0123456789; theme=dark; csrf="csrf-token-abcdef12"`, []string{"cookie-sid-0123456789", "csrf-token-abcdef12"}},
		{"header line", "Authorization: Bearer gw-token-0123456789", []string{"Bearer gw-token-0123456789", "gw-token-0123456789"}},
		{"header pair", "Cookie=sid=cookie-sid-0123456789", []string{"sid=cookie-sid-0123456789", "cookie-sid-0123456789"}},
		{"key and secret", "AKIDEXAMPLE:secret-access-0123", []string{"secret-access-0123"}},
		{"proxy url", "http://alice:proxy-pass-1-9f8e7d@proxy.example:3128", nil},
		{"socks url", "socks5h://operator-account:x@127.0.0.1:1080", nil},
		{"url with a token query", "https://up.example/v1?token=sk-query-0123456789", nil},
		{"plain key", "sk-ant-api03-plain-key-0123456789", nil},
		{"base64 with padding", "c2stdGVzdC9rZXkrNDI=", nil},
		{"short part", "dca:short", nil},
		{"json header line", `"Authorization":"Bearer x"`, nil},
		{"scope", "openid profile email offline_access", nil},
	} {
		if got := SecretParts(c.secret, minFragment); !slices.Equal(got, c.want) {
			t.Errorf("%s: SecretParts(%q) = %q, want %q", c.name, c.secret, got, c.want)
		}
	}
	for secret, want := range map[string][]string{
		"c2stdGVzdC9rZXkrNDI=":                  nil,
		"c2stdGVzdC9rZXkrNDJh==":                nil,
		"Bearer c2stdGVzdC9rZXkrNDJh==":         {"c2stdGVzdC9rZXkrNDJh=="},
		"sid=c2stdGVzdC9rZXkrNDJh==; other=val": {"c2stdGVzdC9rZXkrNDJh==", "val"},
		`sid="=="`:                              nil,
	} {
		if got := SecretParts(secret, 1); !slices.Equal(got, want) {
			t.Errorf("SecretParts(%q, 1) = %q, want %q", secret, got, want)
		}
	}
	for secret, want := range map[string][]string{"dca:short": {"short"}, "Bearer short1": {"short1"}, "theme=dark": {"dark"}, "short-plain": nil} {
		if got := SecretParts(secret, 1); !slices.Equal(got, want) {
			t.Errorf("SecretParts(%q, 1) = %q, want %q", secret, got, want)
		}
	}
}

func TestRedactorRemovesSecretParts(t *testing.T) {
	basic := base64.StdEncoding.EncodeToString([]byte("gw-user:gw-password-123"))
	for _, c := range []struct {
		name string
		c    *Credential
		in   string
		want string
	}{
		{"devin's bare jwt", &Credential{Kind: CredentialAPIKey, APIKey: "devin-session-token$" + partsJWT},
			`{"code":"unauthenticated","message":"invalid JWT ` + partsJWT + `"}`, `{"code":"unauthenticated","message":"invalid JWT [redacted]"}`},
		{"meta's dca part", &Credential{Kind: CredentialOAuth, AccessToken: "meta-minted-key-0123456789", RefreshToken: "dca:meta-device-0123456789"},
			"bad device token meta-device-0123456789", "bad device token [redacted]"},
		{"bearer", &Credential{Kind: "upstream", APIKey: "Bearer gw-token-0123456789"},
			"gateway rejected gw-token-0123456789", "gateway rejected [redacted]"},
		{"basic base64", &Credential{Kind: "upstream", APIKey: "Basic " + basic},
			"bad credentials " + basic, "bad credentials [redacted]"},
		{"basic password", &Credential{Kind: "upstream", APIKey: "Basic " + basic},
			"wrong password gw-password-123", "wrong password [redacted]"},
		{"cookie", &Credential{Kind: "upstream", APIKey: "sid=cookie-sid-0123456789; theme=dark; csrf=csrf-token-abcdef12"},
			"session cookie-sid-0123456789 expired; csrf csrf-token-abcdef12; theme=dark", "session [redacted] expired; csrf [redacted]; theme=dark"},
	} {
		if got := NewRedactor(c.c).String(c.in); got != c.want {
			t.Errorf("%s: String(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestRedactorKeepsPublicPartsOfSecrets(t *testing.T) {
	basic := base64.StdEncoding.EncodeToString([]byte("gw-user:gw-password-123"))
	secrets := []string{
		"devin-session-token$" + partsJWT,
		"dca:meta-device-0123456789",
		"Authorization: Bearer gw-token-0123456789",
		"Bearer gw-token-0123456789",
		"Basic " + basic,
		"Cookie: sid=cookie-sid-0123456789; theme=dark; csrf=csrf-token-abcdef12",
		"http://alice:proxy-pass-1-9f8e7d@proxy.example:3128",
	}
	c := &Credential{Kind: "upstream"}
	c.Data, _ = json.Marshal(secrets)
	r := NewRedactor(c)
	const text = "Bearer token rejected by Authorization: devin-session-token dca: sid theme=dark csrf Cookie: " +
		"Basic auth for gw-user, http://proxy.example:3128 alice, session token invalid"
	if got := r.String(text); got != text {
		t.Errorf("String changed public text:\n got %q\nwant %q", got, text)
	}
	whole := map[string]bool{}
	for _, s := range secrets {
		for _, f := range encodings(s) {
			whole[f] = true
		}
	}
	added := 0
	for _, f := range r.forms {
		if whole[f] {
			continue
		}
		added++
		if len(f) < minFragment {
			t.Errorf("the parts add the form %q, shorter than %d bytes", f, minFragment)
		}
		for _, public := range []string{"Bearer", "Basic", "devin-session-token", "dca", "sid", "theme", "csrf", "Cookie", "Authorization", "alice", "proxy.example"} {
			if f == public {
				t.Errorf("the public part %q is a form", f)
			}
		}
	}
	if added == 0 {
		t.Error("the parts add no form")
	}
	if p := SecretParts(secrets[len(secrets)-1], minFragment); p != nil {
		t.Errorf("SecretParts of a proxy URL = %q, want none", p)
	}
	if strings.Contains(r.String("dark"), Redacted) {
		t.Error("a short cookie value is removed")
	}
}

func TestAccountSecretsBase64Padding(t *testing.T) {
	basicText := base64.StdEncoding.EncodeToString([]byte("gw-user:gw-pass-123"))
	long := "c2VydmljZS1hY2NvdW50LTE6cDdRdzJFcjVhYWFhYWE=="
	for _, c := range []struct {
		name string
		acct Account
		want string
		cut  string
	}{
		{"api key", Account{Credential: &Credential{APIKey: "c2stdGVzdC9rZXkrNDJh=="}}, "c2stdGVzdC9rZXkrNDJh==", "c2stdGVz"},
		{"long api key", Account{Credential: &Credential{APIKey: long}}, long, long[10:30]},
		{"access token", Account{Credential: &Credential{AccessToken: "dG9rZW4tb25lLXR3bw=="}}, "dG9rZW4tb25lLXR3bw==", "dG9rZW4t"},
		{"bearer header", Account{Headers: map[string]string{"Authorization": "Bearer dG9rZW4tb25lLXR3bw=="}}, "dG9rZW4tb25lLXR3bw==", "dG9rZW4t"},
		{"basic header", Account{Headers: map[string]string{"Authorization": "Basic " + basicText}}, basicText, basicText[:8]},
		{"secret setting", Account{Settings: []byte(`{"client_secret":"c2V0dGluZy1zZWNyZXQ="}`)}, "c2V0dGluZy1zZWNyZXQ=", "c2V0dGlu"},
		{"secret setting, double padding", Account{Settings: []byte(`{"client_secret":"c2V0dGluZy1zZWNyZXRh=="}`)}, "c2V0dGluZy1zZWNyZXRh==", "c2V0dGlu"},
	} {
		got := AccountSecrets(c.acct)
		if !slices.Contains(got, c.want) || slices.Contains(got, "=") || slices.Contains(got, "==") {
			t.Errorf("%s: AccountSecrets = %q, want it to hold %q and not the padding", c.name, got, c.want)
		}
		in := "bad credential " + c.cut + " given"
		if out, want := RemovePieces(in, got...), "bad credential [redacted] given"; out != want {
			t.Errorf("%s: RemovePieces(%q) = %q, want %q", c.name, in, out, want)
		}
	}
	got := AccountSecrets(Account{Headers: map[string]string{"Cookie": "sid=cookie-sid-0123456789; theme=dark; other=c2VjcmV0LXZhbHVl=="}})
	slices.Sort(got)
	if want := []string{"c2VjcmV0LXZhbHVl==", "cookie-sid-0123456789"}; !slices.Equal(got, want) {
		t.Errorf("cookie header: AccountSecrets = %q, want %q", got, want)
	}
}
