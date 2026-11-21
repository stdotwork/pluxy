package sdk

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/protocol"
)

// MaxLoginText is how many characters of provider text a login error keeps; longer text is cut
// there and ends in an ellipsis.
const MaxLoginText = 200

var (
	// opaqueRun matches a long run of token characters, the shape of an access token, a refresh
	// token, a JWT, a device code or an API key.
	opaqueRun = regexp.MustCompile(`[A-Za-z0-9_\-.+/=~]{24,}`)
	// bearerValue matches what follows "Bearer" or "Basic".
	bearerValue = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s"',;]+`)
	// secretPair matches name=value or name: value for a name that carries a secret.
	secretPair = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|session_token|token|device_code|authorization_code|user_code|code_verifier|client_secret|api[_-]?key|password|secret)\b(["']?\s*[:=]\s*["']?)[^\s"',;&]+`)
)

// LoginText returns provider text for a login error: one line without control characters, cut
// after MaxLoginText characters with an ellipsis, with long token-like strings (also where JSON or
// percent escapes spell them) and the values of fields named like access_token, token,
// device_code, client_secret or password replaced. Before an
// account has a credential core cannot redact what a provider echoes, so this is the only
// safeguard.
func LoginText(s string) string {
	return boundText(s, MaxLoginText, nil)
}

// MaxUpstreamText is how many characters of upstream error text an error message keeps; longer
// text is cut there and ends in an ellipsis. It leaves room for a few sentences of explanation,
// while an HTML error page stays a short line in the client's error and core's log.
const MaxUpstreamText = 1000

// UpstreamText returns an upstream's error text for an error message, whether the upstream sent
// it as text or as the message of a JSON error: one line, cut after MaxUpstreamText characters
// with an ellipsis. Such text can hold part of a secret the upstream echoed, split across lines or
// cut short. Core itself removes the pieces of the account's secrets (protocol.AccountSecrets)
// from an error's message and raw body by the piece rule below (protocol.RemovePieces); the
// other rules, the bound and the secrets core does not know are this function's. The text is
// cleaned as
// LoginText cleans it: Bearer and Basic values, the values of fields named like a secret, and runs
// of 24 or more token characters are replaced. That misses a shorter piece of a token anywhere
// else, so a caller passes the secrets it knows, AccountSecrets of the call's account and any it
// holds itself: first, every run of token characters that holds 4 or more consecutive characters
// of one, not all of them in its public vendor prefix such as "sk-proj-" (protocol.RemovePieces),
// is replaced, and 8 or more of a long token or of a PEM private key's body. A run is read
// with its escapes decoded too (JSON's \/, \\ and \uXXXX and percent-encoding's %XX, nested up to 8
// times), so a piece written with escapes between its characters is replaced, and so is a run whose
// decoded text holds 24 or more token characters in a row.
func UpstreamText(s string, secrets ...string) string {
	return boundText(s, MaxUpstreamText, secrets)
}

// cleanRuns replaces in s every run of token characters and escapes that, with its escapes
// decoded, holds a piece of one of secrets (protocol.RemovePieces: 4 or more consecutive
// characters of one, or 8 of a long token or of a PEM private key's body), and every run with
// escapes whose decoded text holds a run opaqueRun matches.
func cleanRuns(s string, secrets []string) string {
	s = protocol.RemovePieces(s, secrets...)
	if !strings.ContainsAny(s, `\%`) {
		return s
	}
	return protocol.ReplaceRuns(s, func(run, decoded string) bool {
		return decoded != run && opaqueRun.MatchString(decoded)
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
	s = cleanRuns(s, secrets)
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
