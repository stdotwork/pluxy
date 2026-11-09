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
