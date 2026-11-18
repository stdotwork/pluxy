package sdk

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// An account's secrets are those of its credential, its secret headers, its proxy and its
// settings, each by its secret part where it has a public prefix, scheme or name; plain header
// and settings values, a short cookie value and the public words are not among them.
func TestAccountSecrets(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhY2NvdW50In0.c2VjcmV0cy10ZXN0"
	settings, _ := json.Marshal(map[string]any{
		"base_url":      "https://up.example/v1?token=q7Zx91Lm4kT2&model=gpt-x",
		"extra_headers": map[string]string{"Authorization": "Bearer s9Qw2Er5Ty8Ui1Op"},
		"device_seed":   "seed-not-secret-123",
	})
	a := protocol.Account{
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "devin-session-token$" + jwt},
		Headers: map[string]string{
			"Authorization": "Bearer g4Hj7Kl0Zx3Cv6Bn",
			"X-Gateway-Key": "Gw5Rt8Yu1Io4Pa7Sd0Fg3Hj6",
			"X-Api-Key":     "short",
			"X-Title":       "plain-title-value",
			"Cookie":        "sid=c2Vb5Nm8Qa1Ws4Ed; theme=dark",
		},
		ProxyURL: "http://u3Rf6Tg9Yh2Uj:p8Ik1Ol4Pz7Xc0Vb@127.0.0.1:3128",
		Settings: settings,
	}
	got := AccountSecrets(a)
	slices.Sort(got)
	want := []string{"c2Vb5Nm8Qa1Ws4Ed", jwt, "Gw5Rt8Yu1Io4Pa7Sd0Fg3Hj6", "g4Hj7Kl0Zx3Cv6Bn",
		"p8Ik1Ol4Pz7Xc0Vb", "u3Rf6Tg9Yh2Uj", "q7Zx91Lm4kT2", "s9Qw2Er5Ty8Ui1Op"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("AccountSecrets = %q\nwant %q", got, want)
	}
	// What an upstream echoes of them, in pieces too short for the 24-character rule, goes.
	for in, want := range map[string]string{
		"gateway rejected key Gw5Rt8Yu1Io4 (cut)":                "gateway rejected key [redacted] (cut)",
		"proxy rejected credentials for u3Rf6Tg9: p8Ik1Ol4":      "proxy rejected credentials for [redacted]: [redacted]",
		"cookie c2Vb5Nm8 expired, theme=dark":                    "cookie [redacted] expired, theme=dark",
		"invalid JWT " + jwt[:20] + " (truncated)":               "invalid JWT [redacted] (truncated)",
		"invalid devin-session-token for the gateway, try again": "invalid devin-session-token for the gateway, try again",
	} {
		if got := UpstreamText(in, AccountSecrets(a)...); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
}
