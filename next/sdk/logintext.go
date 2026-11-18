package sdk

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxLoginText is how many characters of provider text a login error keeps; longer text is cut
// there and ends in an ellipsis.
const MaxLoginText = 200

var (
	// opaqueRun matches a long run of token characters, the shape of an access token, a refresh
	// token, a JWT, a device code or an API key.
	opaqueRun = regexp.MustCompile(`[A-Za-z0-9_\-.+/=~]{24,}`)
	// pieceRun matches a run of the same token characters long enough to hold a piece of a secret.
	pieceRun = regexp.MustCompile(`[A-Za-z0-9_\-.+/=~]{4,}`)
	// bearerValue matches what follows "Bearer" or "Basic".
	bearerValue = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s"',;]+`)
	// secretPair matches name=value or name: value for a name that carries a secret.
	secretPair = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|device_code|authorization_code|user_code|code_verifier|client_secret|api[_-]?key|password|secret)\b(["']?\s*[:=]\s*["']?)[^\s"',;&]+`)
)

// LoginText returns provider text for a login error: one line without control characters, cut
// after MaxLoginText characters with an ellipsis, with long token-like strings and the values of
// fields named like access_token, device_code, client_secret or password replaced. Before an
// account has a credential core cannot redact what a provider echoes, so this is the only
// safeguard.
func LoginText(s string) string {
	return boundText(s, MaxLoginText, nil)
}

// MaxUpstreamText is how many characters of upstream error text an error message keeps; longer
// text is cut there and ends in an ellipsis. It leaves room for a few sentences of explanation,
// while an HTML error page stays a short line in the client's error and core's log.
const MaxUpstreamText = 1000

// minPiece is the shortest piece of a secret UpstreamText replaces, as core's TrimCut counts one.
const minPiece = 4

// UpstreamText returns an upstream's error text that is not structured (not JSON with a message)
// for an error message: one line, cut after MaxUpstreamText characters with an ellipsis. Core
// removes only whole copies of an account's credential, and such text can hold part of one the
// upstream echoed, split across lines or cut short. The text is cleaned as LoginText cleans it:
// Bearer and Basic values, the values of fields named like a secret, and runs of 24 or more token
// characters are replaced. That misses a shorter piece of a token anywhere else, so a caller passes
// the secrets it knows: first, every run of token characters that holds 4 or more consecutive
// characters of one is replaced.
func UpstreamText(s string, secrets ...string) string {
	return boundText(s, MaxUpstreamText, secrets)
}

// withoutPieces replaces in s every run of token characters that holds minPiece or more
// consecutive characters of one of secrets.
func withoutPieces(s string, secrets []string) string {
	pieces := map[string]bool{}
	for _, secret := range secrets {
		for i := 0; i+minPiece <= len(secret); i++ {
			pieces[secret[i:i+minPiece]] = true
		}
	}
	if len(pieces) == 0 {
		return s
	}
	return pieceRun.ReplaceAllStringFunc(s, func(run string) string {
		for i := 0; i+minPiece <= len(run); i++ {
			if pieces[run[i:i+minPiece]] {
				return "[redacted]"
			}
		}
		return run
	})
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

func boundText(s string, max int, secrets []string) string {
	s = withoutPieces(s, secrets)
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
