package protocol

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// A run of token characters that holds 4 or more characters of a secret under 32 bytes in a row is replaced,
// also where escapes spell them; the words around it, a run that shares only 3 characters and
// text without any piece are kept.
func TestRemovePieces(t *testing.T) {
	const key = "sk-test-Qx7Rv2Lm9Tz4Wp8Ys3Nk6"
	for in, want := range map[string]string{
		"invalid x-api-key: " + key[:24] + "...":               "invalid x-api-key: [redacted]",
		"Incorrect API key provided: " + key[:12] + "*****Nk6": "Incorrect API key provided: [redacted]*****Nk6",
		`key Qx7\u0052v2 given`:                                "key [redacted] given",
		"key Qx7%252Fv2 given":                                 "key Qx7%252Fv2 given", // decodes to Qx7/v2: no 4 characters of the key
		"key Qx7R%76 given":                                    "key [redacted] given",
		"rate limited, try again in 7 seconds":                 "rate limited, try again in 7 seconds",
		"Qx7 and Rv2 and Lm9":                                  "Qx7 and Rv2 and Lm9",
	} {
		if got := RemovePieces(in, key); got != want {
			t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
		}
	}
	if got := RemovePieces("key Qx7Rv2Lm"); got != "key Qx7Rv2Lm" {
		t.Errorf("without secrets: %q", got)
	}
	if got := RemovePieces("key abc given", "abc"); got != "key abc given" {
		t.Errorf("a secret under 4 characters: %q", got)
	}
}

// A key's public vendor prefix gives no piece: the error keeps "project" for an "sk-proj-" key,
// while a cut piece of the key's random part is still removed.
func TestRemovePiecesKeepsPublicPrefix(t *testing.T) {
	const key = "sk-proj-Qx7Rv2Lm9Tz4Wp8Ys3Nk"
	in := "project not found for key " + key[:20] + "..."
	if got, want := RemovePieces(in, key), "project not found for key [redacted]"; got != want {
		t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
	}
	if got := RemovePieces("the kimi-k2 model is busy", "sk-kimi-Ab3Cd5Ef7Gh9Ij1Kl3"); got != "the kimi-k2 model is busy" {
		t.Errorf("a word of the prefix went: %q", got)
	}
}

// A key file's private key spans lines: its pieces are taken a line at a time, at 8 bytes, so a
// key that spells part of an error's word leaves the word whole, while the key itself, each of its
// lines and a line cut in half are still removed.
func TestRemovePiecesKeyFileLines(t *testing.T) {
	const (
		line1 = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQInvaA7Zs3Lp9Qd"
		line2 = "v2Wp8Ys3Nk6Qd4Rf1Gh5Ij7Kl2Mn0Op3Qr6St9Uv8Wx1Yz4Ab5Cd7Ef0Gh2Ij5K"
		line3 = "0Zt3Xc6Vb9Nm2Qw5Er8Ty1Ui4Op7As0Df3Gh6Jk9LzXc5VbQSign5Nm1Qw4Er7Ty"
	)
	key := "-----BEGIN PRIVATE KEY-----\n" + line1 + "\n" + line2 + "\n" + line3 + "\n-----END PRIVATE KEY-----\n"
	p := AccountSecretsWith(Account{}, []string{key}) // as core passes a key file's private_key
	if in := "invalid_grant (Invalid JWT Signature.)"; RemovePieces(in, p...) != in {
		t.Errorf("RemovePieces(%q) = %q, want the words kept", in, RemovePieces(in, p...))
	}
	for _, line := range []string{line1, line2, line3} {
		if got := RemovePieces(line, p...); got != Redacted {
			t.Errorf("RemovePieces(%q) = %q, want %q", line, got, Redacted)
		}
	}
	// A PEM's boundary lines are public words, so of them only what a run holds longPiece bytes
	// of goes: "PRIVATE", 7 bytes, is no piece, and the key's body is gone whole.
	whole := "private_key: " + key
	want := "private_key: [redacted] PRIVATE [redacted]\n" + strings.Repeat(Redacted+"\n", 3) + "[redacted] PRIVATE [redacted]\n"
	if got := RemovePieces(whole, p...); got != want {
		t.Errorf("RemovePieces(%q) = %q, want %q", whole, got, want)
	}
	if half := "cut " + line2[:32]; RemovePieces(half, p...) != "cut "+Redacted {
		t.Errorf("RemovePieces(%q) = %q, want the half line removed", half, RemovePieces(half, p...))
	}
}

// A secret whose rest after the prefix is under 16 bytes keeps every piece, the prefix's too.
func TestRemovePiecesShortSecretKeepsPrefixPieces(t *testing.T) {
	const key = "sk-proj-Qx7Rv2Lm9Tz4" // 12 bytes after the prefix
	if got, want := RemovePieces("project not found", key), "[redacted] not found"; got != want {
		t.Errorf("RemovePieces = %q, want %q", got, want)
	}
}

// publicKeyPrefix takes the longest known vendor prefix a secret starts with, and only when 16
// bytes or more follow it.
func TestPublicKeyPrefix(t *testing.T) {
	const rest = "Qx7Rv2Lm9Tz4Wp8Ys3Nk"
	for in, want := range map[string]int{
		"sk-" + rest:               3,
		"sk-proj-" + rest:          8,
		"sk-svcacct-" + rest:       11,
		"sk-admin-" + rest:         9,
		"sk-ant-api03-" + rest:     13,
		"sk-ant-admin01-" + rest:   15,
		"sk-ant-oat01-" + rest:     13,
		"sk-ant-ort01-" + rest:     13,
		"sk-ant-" + rest:           7,
		"sk-kimi-" + rest:          8,
		"sk-or-v1-" + rest:         9,
		"xai-" + rest:              4,
		"gsk_" + rest:              4,
		"sk-test-" + rest:          3, // only the vendor's "sk-"
		"github_pat_" + rest:       0, // not on the list
		"Sk-" + rest:               0,
		rest:                       0,
		"sk-proj-Qx7Rv2Lm9Tz4Wp8":  0, // 15 bytes after the prefix
		"sk-proj-Qx7Rv2Lm9Tz4Wp8Y": 8,
	} {
		if got := publicKeyPrefix(in); got != want {
			t.Errorf("publicKeyPrefix(%q) = %d, want %d", in, got, want)
		}
	}
}

// Secrets that only look like they start with a prefix keep every piece: a UUID's first groups,
// the words of a secret that is no vendor's key, and the start of an OpenAI key's random part
// after "sk-proj-".
func TestRemovePiecesNoPrefixGuess(t *testing.T) {
	for _, c := range []struct{ secret, in, want string }{
		{"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", "unknown id a1b2c3d4 given", "unknown id [redacted] given"},
		{"my-team-token-Qx7Rv2Lm9Tz4Wp8Ys3", "bad team-token", "bad [redacted]"},
		{"sk-proj-abc1-Qx7Rv2Lm9Tz4Wp8Ys3", "key abc1 refused", "key [redacted] refused"},
	} {
		if got := RemovePieces(c.in, c.secret); got != c.want {
			t.Errorf("RemovePieces(%q, %q) = %q, want %q", c.in, c.secret, got, c.want)
		}
	}
}

// A secret that shares 4 characters with "redacted" leaves an earlier [redacted] as it is.
func TestRemovePiecesKeepsRedacted(t *testing.T) {
	const key = "sk-Zredacted9Qx7Rv2Lm9Tz4"
	in := "bad key " + Redacted + " and " + key[:12]
	if got, want := RemovePieces(in, key), "bad key [redacted] and [redacted]"; got != want {
		t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
	}
}

// A secret that itself holds what reads as an escape is caught as written too.
func TestRemovePiecesLiteralEscape(t *testing.T) {
	const key = "Zq%41Wx9Lm"
	if got, want := RemovePieces("bad key Zq%41W", key), "bad key [redacted]"; got != want {
		t.Errorf("RemovePieces = %q, want %q", got, want)
	}
}

// A URL below a secret-named settings key counts by its user name, password and secret query
// values only, so an error keeps "https", "auth" and "token"; a query value named like a secret by
// core's rule for settings (sig) counts.
func TestAccountSecretsSettingsURLs(t *testing.T) {
	settings := []byte(`{"token_url":"https://auth.example.com/oauth/token","auth_base_url":"https://u7Qx2Lm9Rv:p4Zw8Kt3Jn@gw.example.com/v1?sig=Sg5Tb2Nq8Vc1Xz&model=m"}`)
	got := AccountSecrets(Account{Settings: settings})
	slices.Sort(got)
	want := []string{"Sg5Tb2Nq8Vc1Xz", "p4Zw8Kt3Jn", "u7Qx2Lm9Rv"}
	if !slices.Equal(got, want) {
		t.Errorf("AccountSecrets = %q, want %q", got, want)
	}
	in := "POST https://auth.example.com/oauth/token failed: token expired"
	if out := RemovePieces(in, got...); out != in {
		t.Errorf("RemovePieces(%q) = %q", in, out)
	}
	with := AccountSecretsWith(Account{}, []string{"https://u7Qx2Lm9Rv:p4Zw8Kt3Jn@gw.example.com/v1", "Plain0Secret9Value"})
	slices.Sort(with)
	if want := []string{"Plain0Secret9Value", "p4Zw8Kt3Jn", "u7Qx2Lm9Rv"}; !slices.Equal(with, want) {
		t.Errorf("AccountSecretsWith = %q, want %q", with, want)
	}
}

// ReplaceRuns hands each run to match as written and decoded, through nested escapes, and
// replaces the runs match reports.
func TestReplaceRuns(t *testing.T) {
	var seen [][2]string
	got := ReplaceRuns(`a %2541b \ud83d\ude00 c\/d end`, func(run, decoded string) bool {
		seen = append(seen, [2]string{run, decoded})
		return decoded == "c/d"
	})
	if got != `a %2541b \ud83d\ude00 [redacted] end` {
		t.Errorf("ReplaceRuns = %q", got)
	}
	want := [][2]string{{"a", "a"}, {"%2541b", "Ab"}, {`\ud83d\ude00`, "😀"}, {`c\/d`, "c/d"}, {"end", "end"}}
	if len(seen) != len(want) {
		t.Fatalf("runs %q, want %q", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("run %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

// Only a PEM private key counts by its lines. Any other secret with a line break inside it keeps
// the 4-byte rule whatever its length, and so does a short token read from a file with its final
// newline: an upstream that echoes a refused key cut to its first characters must not get them
// through.
func TestRemovePiecesOnlyPEMCountsByLines(t *testing.T) {
	for _, secret := range []string{"tok-abcdefghijklmnopqrstuvwxyz\n", "first-line-secret\nsecond-line-secret"} {
		if got := RemovePieces("key tok-a... refused", secret); strings.HasPrefix(secret, "tok-") && got != "key [redacted] refused" {
			t.Errorf("RemovePieces with %q = %q, want the 5-byte cut removed", secret, got)
		}
		if got := RemovePieces("key seco... refused", secret); !strings.HasPrefix(secret, "tok-") && got != "key [redacted] refused" {
			t.Errorf("RemovePieces with %q = %q, want the 4-byte cut removed", secret, got)
		}
	}
}

// Only one long token counts at 8 bytes. A value of 32 bytes or more that holds a shorter token
// in a shape SecretParts does not split, or that is the base64 of text such as Basic's
// user:password, keeps the 4-byte rule: a few bytes of it can be a large share of the real secret.
func TestRemovePiecesLongRuleIsForOneToken(t *testing.T) {
	account := func(name, value string) []string {
		return AccountSecrets(Account{Headers: map[string]string{name: value}})
	}
	longText := base64.StdEncoding.EncodeToString([]byte("a-long-service-account-name:p7Qw2Er5"))
	for _, c := range []struct {
		name    string
		secrets []string
		in      string
	}{
		{"another scheme", account("Authorization", "Token Ab3Cd5Ef7Gh9Ij1Kl3Mn5Op7Qr"), "invalid key: Ab3C given"},
		{"JSON in a header", account("Authorization", `{"api_key":"Ab3Cd5Ef","user":"svc"}`), "refused Ab3C given"},
		{"a pasted header line", []string{"Authorization: Token Ab3Cd5Ef7Gh9Ij1Kl3Mn5Op7Qr"}, "refused Ab3C given"},
		{"Basic's base64", account("Authorization", "Basic c2VydmljZS1hY2NvdW50LTE6cDdRdzJFcjU="), "invalid credential cDdR given"},
		{"URL-safe base64", []string{"c2VydmljZS1hY2NvdW50LTE6cDdRdzJFcjU-"}, "refused c2Vy given"},
		{"Basic with a Latin-1 password", []string{"c2VydmljZS1hY2NvdW50LTE66enp6enp6ek="}, "refused c2Vy given"},
		{"base64 of longer text", []string{longText}, "invalid credential " + longText[len(longText)-8:len(longText)-4] + " given"},
	} {
		if got, want := RemovePieces(c.in, c.secrets...), strings.Replace(c.in, strings.Fields(c.in)[len(strings.Fields(c.in))-2], Redacted, 1); got != want {
			t.Errorf("%s (secrets %q): RemovePieces(%q) = %q, want %q", c.name, c.secrets, c.in, got, want)
		}
	}
	// A long token keeps the 8-byte rule with its final newline.
	const key = "Qx7Rv2Lm9Tz4Kp8Wn3Hc6Yd1Bf5Gs0Ja\n"
	if got := RemovePieces("key Qx7Rv2L given", key); got != "key Qx7Rv2L given" {
		t.Errorf("a 7-byte cut of a long token with a newline = %q, want it kept", got)
	}
	if got := RemovePieces("key Qx7Rv2Lm given", key); got != "key [redacted] given" {
		t.Errorf("an 8-byte cut of a long token with a newline = %q, want it removed", got)
	}
}

// A fragment that runs across a PEM body's line break, as an upstream that unwraps the key would
// echo it, is removed though neither side holds 8 bytes of one line.
func TestRemovePiecesPEMAcrossLines(t *testing.T) {
	const (
		line1 = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQInvaA7Zs3Lp9Qd"
		line2 = "v2Wp8Ys3Nk6Qd4Rf1Gh5Ij7Kl2Mn0Op3Qr6St9Uv8Wx1Yz4Ab5Cd7Ef0Gh2Ij5K"
	)
	key := "-----BEGIN PRIVATE KEY-----\n" + line1 + "\n" + line2 + "\n-----END PRIVATE KEY-----\n"
	if in := "cut " + line1[len(line1)-5:] + line2[:5]; RemovePieces(in, key) != "cut "+Redacted {
		t.Errorf("RemovePieces(%q) = %q, want the fragment across the line break removed", in, RemovePieces(in, key))
	}
}

// A secret of 32 bytes or more counts at 8 bytes: a 7-byte run of it stays, an 8-byte run is
// replaced, also written with escapes. A shorter one counts at 4, and a trailing newline (a token
// read from a file) is not part of its length.
func TestRemovePiecesLongSecretCountsAtEightBytes(t *testing.T) {
	const long = "Qx7Rv2Lm9Tz4Wp8Ys3Nk6Df1Gh5Jc0Bv" // 32 bytes
	if len(long) != 32 {
		t.Fatalf("test key is %d bytes", len(long))
	}
	short := long[:31]
	for _, c := range []struct {
		name, secret, in, want string
	}{
		{"7 bytes stay", long, "key Qx7Rv2L given", "key Qx7Rv2L given"},
		{"4 bytes stay", long, "key Qx7R given", "key Qx7R given"},
		{"8 bytes go", long, "key Qx7Rv2Lm given", "key [redacted] given"},
		{"8 bytes go, mid-key", long, "key Lm9Tz4Wp given", "key [redacted] given"},
		{"8 bytes with unicode escapes", long, `key Qx7Rv2Lm given`, "key [redacted] given"},
		{"7 bytes with unicode escapes stay", long, `key Qx7Rv2L given`, `key Qx7Rv2L given`},
		{"8 bytes with percent escapes", long, "key Qx7Rv2L%6d given", "key [redacted] given"},
		{"7 bytes with percent escapes stay", long, "key Qx7Rv2%4c given", "key Qx7Rv2%4c given"},
		{"31 bytes: 4 bytes go", short, "key Qx7R given", "key [redacted] given"},
		{"31 bytes and a newline: 4 bytes go", short + "\n", "key Qx7R given", "key [redacted] given"},
		{"31 bytes and a newline: 3 bytes stay", short + "\n", "key Qx7 given", "key Qx7 given"},
		{"32 bytes and a newline: 7 bytes stay", long + "\n", "key Qx7Rv2L given", "key Qx7Rv2L given"},
		{"32 bytes and a newline: 8 bytes go", long + "\r\n", "key Qx7Rv2Lm given", "key [redacted] given"},
		{"31 bytes and spaces around: 4 bytes go", "  " + short + "  ", "key Qx7R given", "key [redacted] given"},
	} {
		if got := RemovePieces(c.in, c.secret); got != c.want {
			t.Errorf("%s: RemovePieces(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// The word "test" in an error stays for a long key that holds it, the way an account's last error
// shows it; a short key that holds it still takes it.
func TestRemovePiecesLongKeyKeepsOrdinaryWords(t *testing.T) {
	const long = "sk-live-test-Qx7Rv2Lm9Tz4Wp8Ys3Nk6Df1"
	in := "test request failed: invalid x-api-key"
	if got := RemovePieces(in, long); got != in {
		t.Errorf("RemovePieces(%q) = %q, want it kept", in, got)
	}
	if got, want := RemovePieces("test request failed", "my-test-key-12"), "[redacted] request failed"; got != want {
		t.Errorf("short key: RemovePieces(%q) = %q, want %q", "test request failed", got, want)
	}
}

// A secret made of parts counts each part by its own length: a part under 32 bytes of a value
// over 32 still counts at 4.
func TestRemovePiecesPartOfLongValueCountsByItsOwnLength(t *testing.T) {
	const part = "Qx7Rv2Lm9Tz4"
	const longPart = "Ab3Cd5Ef7Gh9Ij1Kl3Mn5Op7Qr9St1Uv3Wx"
	a := Account{Headers: map[string]string{"Cookie": "sid=" + part + "; auth=" + longPart}}
	if v := a.Headers["Cookie"]; len(v) <= 32 {
		t.Fatalf("value is %d bytes, want over 32", len(v))
	}
	secrets := AccountSecrets(a)
	if !slices.Contains(secrets, part) || !slices.Contains(secrets, longPart) {
		t.Fatalf("AccountSecrets = %q, want both parts", secrets)
	}
	for in, want := range map[string]string{
		"key Qx7R given":     "key [redacted] given",
		"key Ab3Cd5E given":  "key Ab3Cd5E given",
		"key Ab3Cd5Ef given": "key [redacted] given",
	} {
		if got := RemovePieces(in, secrets...); got != want {
			t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
		}
	}
}

// A long key with a public prefix: a run inside the prefix stays, and an 8-byte run with one byte
// after the prefix is replaced.
func TestRemovePiecesLongKeyWithPrefix(t *testing.T) {
	const key = "sk-proj-Qx7Rv2Lm9Tz4Wp8Ys3Nk6Df1Gh5Jc0Bv"
	for in, want := range map[string]string{
		"project not found":             "project not found",
		"key sk-proj- given":            "key sk-proj- given",
		"key sk-proj-Q given":           "key [redacted] given",
		"key roj-Qx7R given":            "key [redacted] given",
		"key -proj-Q given":             "key -proj-Q given",
		"key k-proj-Q given":            "key [redacted] given",
		"key proj-Qx7 given":            "key [redacted] given",
		"key Qx7Rv2L given":             "key Qx7Rv2L given",
		"key Qx7Rv2Lm given":            "key [redacted] given",
		"key sk-proj-Qx7Rv2Lm9Tz given": "key [redacted] given",
	} {
		if got := RemovePieces(in, key); got != want {
			t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
		}
	}
}
