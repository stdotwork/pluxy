package sdk

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxLoginText is the longest provider text a login error carries.
const MaxLoginText = 200

var (
	// opaqueRun matches a long run of token characters, the shape of an access token, a refresh
	// token, a JWT, a device code or an API key.
	opaqueRun = regexp.MustCompile(`[A-Za-z0-9_\-.+/=~]{24,}`)
	// bearerValue matches what follows "Bearer" or "Basic".
	bearerValue = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s"',;]+`)
	// secretPair matches name=value or name: value for a name that carries a secret.
	secretPair = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|device_code|authorization_code|user_code|code_verifier|client_secret|api[_-]?key|password|secret)\b(["']?\s*[:=]\s*["']?)[^\s"',;&]+`)
)

// LoginText returns provider text for a login error: one line of at most MaxLoginText characters
// without control characters, with long token-like strings and the values of fields named like
// access_token, device_code, client_secret or password replaced. Before an account has a credential core cannot redact what a provider echoes,
// so this is the only safeguard.
func LoginText(s string) string {
	return boundText(s, MaxLoginText)
}

// LoginCode returns a provider's OAuth error code, kept only if it looks like one: at most 64
// letters, digits, and "_", "-", ".", ":".
func LoginCode(s string) string {
	if len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == ':') {
			return ""
		}
	}
	return s
}

func boundText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = bearerValue.ReplaceAllString(s, "$1 [redacted]")
	s = secretPair.ReplaceAllString(s, "$1$2[redacted]")
	s = opaqueRun.ReplaceAllString(s, "[redacted]")
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}
