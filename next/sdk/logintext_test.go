package sdk

import (
	"strings"
	"testing"
)

func TestLoginText(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLWJ5dGVz"
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain text stays", "The code has expired.", "The code has expired."},
		{"newlines and tabs become spaces", "line one\r\n\tline two\x00", "line one line two"},
		{"a JWT", "bad token " + jwt + " given", "bad token [redacted] given"},
		{"a long opaque string", "refresh abcdefghijklmnopqrstuvwxyz0123456789 failed", "refresh [redacted] failed"},
		{"a bearer value", "Authorization: Bearer shortvalue", "Authorization: Bearer [redacted]"},
		{"a named field", `invalid request: access_token=abc123 and "client_secret": "s3cr3t"`, `invalid request: access_token=[redacted] and "client_secret": "[redacted]"`},
		{"a device code", "device_code: 4f5g6h", "device_code: [redacted]"},
		{"an authorization code", "bad authorization_code=abc123 given", "bad authorization_code=[redacted] given"},
		{"a code verifier", `code_verifier":"short1"`, `code_verifier":"[redacted]"`},
	} {
		if got := LoginText(tc.in); got != tc.want {
			t.Errorf("%s: LoginText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	long := LoginText(strings.Repeat("word ", 400))
	if n := len([]rune(long)); n > MaxLoginText+1 {
		t.Errorf("a long text is %d characters, want at most %d", n, MaxLoginText+1)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("a cut text should end in an ellipsis: %q", long)
	}
}

func TestUpstreamText(t *testing.T) {
	for in, want := range map[string]string{
		"service overloaded, try again later":                   "service overloaded, try again later",
		"bad token\nabcdefghijklmnopqrstuvwxyz0123456789 given": "bad token [redacted] given",
		"Authorization: Basic short-start":                      "Authorization: Basic [redacted]",
		"invalid session token abcdefghijklmnopqrstuv... (cut)": "invalid session token [redacted] (cut)",
	} {
		if got := UpstreamText(in); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
	text := strings.Repeat("word ", 150)
	if got := UpstreamText(text); got != strings.TrimSpace(text) {
		t.Errorf("a 749-character text was changed: %d characters", len([]rune(got)))
	}
	if got, want := UpstreamText(strings.Repeat("ab ", 700)), strings.Repeat("ab ", 333)+"a…"; got != want {
		t.Errorf("a 2099-character text became %d characters ending %q, want %d", len([]rune(got)), got[max(0, len(got)-8):], len([]rune(want)))
	}
	if got, want := UpstreamText(strings.Repeat("éa ", 700)), strings.Repeat("éa ", 333)+"é…"; got != want {
		t.Errorf("a 2099-character text of 2800 bytes became %d characters, want %d", len([]rune(got)), len([]rune(want)))
	}
}

func TestUpstreamTextSecretPieces(t *testing.T) {
	const secret = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1In0.c2lnbmF0dXJl"
	for in, want := range map[string]string{
		"token was eyJhbGciOiJIUzI":              "token was [redacted]",
		"bad credential c2lnbmF0 given":          "bad credential [redacted] given",
		"bad credential c2lnbmF given":           "bad credential c2lnbmF given",
		"joined F0dXJl-devin-ses":                "joined F0dXJl-devin-ses",
		"joined bmF0dXJl-devin-ses":              "joined [redacted]",
		"invalid session token, try again later": "invalid session token, try again later",
	} {
		if got := UpstreamText(in, secret); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := UpstreamText("token was eyJhbGciOiJIUzI"); got != "token was eyJhbGciOiJIUzI" {
		t.Errorf("without the secret, a 15-character piece was changed: %q", got)
	}
	if got := LoginText("token was eyJhbGciOiJIUzI"); got != "token was eyJhbGciOiJIUzI" {
		t.Errorf("LoginText changed a 15-character run: %q", got)
	}
}

func TestLoginCode(t *testing.T) {
	for in, want := range map[string]string{
		"invalid_grant":                         "invalid_grant",
		"urn:ietf:params:oauth:error:bad.thing": "urn:ietf:params:oauth:error:bad.thing",
		"has space":                             "",
		"new\nline":                             "",
		strings.Repeat("a", 65):                 "",
		"":                                      "",
	} {
		if got := LoginCode(in); got != want {
			t.Errorf("LoginCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpstreamTextEscapedPieces(t *testing.T) {
	const secret = "tok/ab/cd/ef/0123456789"
	for in, want := range map[string]string{
		`bad key tok\/ab\/cd\/ef given`:             "bad key [redacted] given",
		"bad key tok%2Fab%2Fcd%2Fef given":          "bad key [redacted] given",
		"bad key tok%252Fab%252Fcd given":           "bad key [redacted] given",
		`bad key \u0074\u006f\u006b\u002f given`:    "bad key [redacted] given",
		`bad key 0\\\/12\\\/3 given`:                `bad key 0\\\/12\\\/3 given`,
		"path%20with%20spaces and a \\/ slash":      "path%20with%20spaces and a \\/ slash",
		"refused abcdefgh%2Fijklmnop%2Fqrstuvwx%2F": "refused [redacted]",
	} {
		if got := UpstreamText(in, secret); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := UpstreamText(`token a1b2c3d4\/e5f6g7h8\/i9j0k1l2\/m3`); got != "token [redacted]" {
		t.Errorf("an escaped 30-character run became %q", got)
	}
}

func TestUpstreamTextTokenFields(t *testing.T) {
	for in, want := range map[string]string{
		"bad session_token=zzz-short-1 given": "bad session_token=[redacted] given",
		`{"token": "abc123"} refused`:         `{"token": "[redacted]"} refused`,
		"max_token=5 and tokens: 5 left":      "max_token=5 and tokens: 5 left",
		"invalid token, try again":            "invalid token, try again",
	} {
		if got := UpstreamText(in); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
}
