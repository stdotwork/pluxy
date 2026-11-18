package protocol

import "testing"

// A run of token characters that holds 4 or more characters of a secret in a row is replaced,
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

// A secret whose rest after the prefix is under 16 bytes keeps every piece, the prefix's too.
func TestRemovePiecesShortSecretKeepsPrefixPieces(t *testing.T) {
	const key = "sk-proj-Qx7Rv2Lm9Tz4" // 12 bytes after the prefix
	if got, want := RemovePieces("project not found", key), "[redacted] not found"; got != want {
		t.Errorf("RemovePieces = %q, want %q", got, want)
	}
}

// publicKeyPrefix takes whole lowercase groups ending in "-" or "_", at most 16 bytes, and only
// when 16 bytes or more follow.
func TestPublicKeyPrefix(t *testing.T) {
	const rest = "Qx7Rv2Lm9Tz4Wp8Ys3Nk"
	for in, want := range map[string]int{
		"sk-" + rest:               3,
		"sk-proj-" + rest:          8,
		"sk-svcacct-" + rest:       11,
		"sk-ant-api03-" + rest:     13,
		"xai-" + rest:              4,
		"github_pat_" + rest:       11,
		"sk-or-v1-abcdefg-" + rest: 9, // the next group would pass 16 bytes
		"Sk-" + rest:               0,
		"1sk-" + rest:              0,
		"sk--" + rest:              3,
		rest:                       0,
		"sk-proj-Qx7Rv2Lm9Tz4Wp8":  0, // 15 bytes after the prefix
		"sk-proj-Qx7Rv2Lm9Tz4Wp8Y": 8,
	} {
		if got := publicKeyPrefix(in); got != want {
			t.Errorf("publicKeyPrefix(%q) = %d, want %d", in, got, want)
		}
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
