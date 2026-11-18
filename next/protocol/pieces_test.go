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
