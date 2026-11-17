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

// Upstream error text is cleaned as login text is, and may run longer.
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
	text := strings.Repeat("word ", 150) // 749 characters, more than login text keeps
	if got := UpstreamText(text); got != strings.TrimSpace(text) {
		t.Errorf("a 749-character text was changed: %d characters", len([]rune(got)))
	}
	// 2099 characters are cut to the first 1000 and an ellipsis; so are 2099 characters of 2800
	// bytes, which a cap counted in bytes would cut at 750 characters.
	if got, want := UpstreamText(strings.Repeat("ab ", 700)), strings.Repeat("ab ", 333)+"a…"; got != want {
		t.Errorf("a 2099-character text became %d characters ending %q, want %d", len([]rune(got)), got[max(0, len(got)-8):], len([]rune(want)))
	}
	if got, want := UpstreamText(strings.Repeat("éa ", 700)), strings.Repeat("éa ", 333)+"é…"; got != want {
		t.Errorf("a 2099-character text of 2800 bytes became %d characters, want %d", len([]rune(got)), len([]rune(want)))
	}
}

// A secret the caller passes is removed in pieces: every run of token characters that holds 4 or
// more consecutive characters of it, however short, and only those.
func TestUpstreamTextSecretPieces(t *testing.T) {
	const secret = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1In0.c2lnbmF0dXJl"
	for in, want := range map[string]string{
		"token was eyJhbGciOiJIUzI":              "token was [redacted]",
		"bad credential c2ln given":              "bad credential [redacted] given",
		"joined XJl-devin-ses":                   "joined XJl-devin-ses", // 3 characters of it: not a piece
		"joined dXJl-devin-ses":                  "joined [redacted]",    // 4 characters of it
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
