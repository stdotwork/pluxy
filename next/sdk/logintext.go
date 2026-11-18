package sdk

import (
	"regexp"
	"strings"
	"unicode/utf16"
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
	secretPair = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|session_token|token|device_code|authorization_code|user_code|code_verifier|client_secret|api[_-]?key|password|secret)\b(["']?\s*[:=]\s*["']?)[^\s"',;&]+`)
	// escapedRun matches a run of token characters and of the escapes that can spell them: JSON's
	// \/, \\ and \uXXXX, and percent-encoding's %XX, nested or not.
	escapedRun = regexp.MustCompile(`(?:[A-Za-z0-9_\-.+/=~]|\\u[0-9A-Fa-f]{4}|\\[\\/]|%[0-9A-Fa-f]{2})+`)
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

// minPiece is the shortest piece of a secret UpstreamText replaces, as core's TrimCut counts one.
const minPiece = 4

// UpstreamText returns an upstream's error text for an error message, whether the upstream sent
// it as text or as the message of a JSON error: one line, cut after MaxUpstreamText characters
// with an ellipsis. Core removes only whole copies of an account's secrets, and such text can hold
// part of one the upstream echoed, split across lines or cut short. The text is cleaned as
// LoginText cleans it: Bearer and Basic values, the values of fields named like a secret, and runs
// of 24 or more token characters are replaced. That misses a shorter piece of a token anywhere
// else, so a caller passes the secrets it knows, AccountSecrets of the call's account and any it
// holds itself: first, every run of token characters that holds 4 or more consecutive characters
// of one is replaced. A run is read with its escapes decoded too (JSON's \/, \\ and \uXXXX and
// percent-encoding's %XX, nested up to 8 times), so a piece written with escapes between its
// characters is replaced, and so is a run whose decoded text holds 24 or more token characters
// in a row.
func UpstreamText(s string, secrets ...string) string {
	return boundText(s, MaxUpstreamText, secrets)
}

// maxDecode bounds how many layers of escapes cleanRuns decodes.
const maxDecode = 8

// cleanRuns replaces in s every run of token characters and escapes (escapedRun) that, with its
// escapes decoded, holds minPiece or more consecutive characters of one of secrets, and every run
// with escapes whose decoded text holds a run opaqueRun matches.
func cleanRuns(s string, secrets []string) string {
	pieces := map[string]bool{}
	for _, secret := range secrets {
		for i := 0; i+minPiece <= len(secret); i++ {
			pieces[secret[i:i+minPiece]] = true
		}
	}
	if len(pieces) == 0 && !strings.ContainsAny(s, `\%`) {
		return s
	}
	return escapedRun.ReplaceAllStringFunc(s, func(run string) string {
		d := run
		if strings.ContainsAny(run, `\%`) {
			d = decodeRun(run)
			if opaqueRun.MatchString(d) {
				return "[redacted]"
			}
		}
		for i := 0; i+minPiece <= len(d); i++ {
			if pieces[d[i:i+minPiece]] {
				return "[redacted]"
			}
		}
		return run
	})
}

// decodeRun returns run with its escapes decoded, layer by layer, until no escape is left or
// after maxDecode layers.
func decodeRun(run string) string {
	for range maxDecode {
		d := decodeOnce(run)
		if d == run {
			break
		}
		run = d
	}
	return run
}

// decodeOnce decodes one layer of the escapes escapedRun matches: \uXXXX (a surrogate pair as
// one character), \/ and \\, and %XX.
func decodeOnce(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
		case s[i] == '\\' && i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '\\'):
			b.WriteByte(s[i+1])
			i++
		case s[i] == '\\' && i+5 < len(s) && s[i+1] == 'u' && hex4(s[i+2:i+6]):
			r := rune(hexValue(s[i+2 : i+6]))
			i += 5
			if utf16.IsSurrogate(r) && i+6 < len(s) && s[i+1] == '\\' && s[i+2] == 'u' && hex4(s[i+3:i+7]) {
				if p := utf16.DecodeRune(r, rune(hexValue(s[i+3:i+7]))); p != utf8.RuneError {
					r = p
					i += 6
				}
			}
			b.WriteRune(r)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

func hex4(s string) bool {
	return len(s) == 4 && isHex(s[0]) && isHex(s[1]) && isHex(s[2]) && isHex(s[3])
}

func hexValue(s string) int {
	v := 0
	for i := range len(s) {
		v = v<<4 | int(unhex(s[i]))
	}
	return v
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
