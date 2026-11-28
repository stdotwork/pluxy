package sdk

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

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

func TestAccountSecretsShortParts(t *testing.T) {
	a := protocol.Account{
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "devin-session-token$short"},
		Headers:    map[string]string{"Authorization": "Bearer short1", "Cookie": "theme=dark"},
	}
	got, want := AccountSecrets(a), []string{"Bearer short1", "short", "theme=dark"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("AccountSecrets = %q, want %q", got, want)
	}
	for in, want := range map[string]string{
		"this session refuses Bearer, and the theme is dark": "this session refuses [redacted], and the [redacted] is [redacted]",
		"invalid devin-session-token: shor (truncated)":      "invalid devin-session-token: [redacted] (truncated)",
	} {
		if got := UpstreamText(in, AccountSecrets(a)...); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccountSecretsPartsHoldingParts(t *testing.T) {
	basic := base64.StdEncoding.EncodeToString([]byte("ops@gw.example:gw-password-123"))
	settings, _ := json.Marshal(map[string]string{"api_key": "X-Gateway: Bearer gw-token-0123456789"})
	a := protocol.Account{Headers: map[string]string{
		"Cookie":        "sid=session123; csrf=session123-tail99",
		"Authorization": "Basic " + basic,
	}, Settings: settings}
	got := AccountSecrets(a)
	slices.Sort(got)
	want := []string{basic, "gw-password-123", "gw-token-0123456789", "session123", "session123-tail99"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("AccountSecrets = %q\nwant %q", got, want)
	}
	for in, want := range map[string]string{
		"csrf tail99 rejected":                       "csrf [redacted] rejected",
		"user ops@gw.example is not known":           "user ops@gw.example is not known",
		"the gateway refuses Bearer, use a key":      "the gateway refuses Bearer, use a key",
		"the gateway rejected gw-token-0123 (short)": "the gateway rejected [redacted] (short)",
	} {
		if got := UpstreamText(in, AccountSecrets(a)...); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccountSecretsWrappersOfShortParts(t *testing.T) {
	settings, _ := json.Marshal(map[string]string{"api_key": "X-Gateway: Bearer s12"})
	basic := base64.StdEncoding.EncodeToString([]byte("ops@gw.example:"))
	a := protocol.Account{
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "Authorization: Bearer short1"},
		Headers:    map[string]string{"Authorization": "Basic " + basic, "Cookie": "c=devin-session-token$s12"},
		Settings:   settings,
	}
	got := AccountSecrets(a)
	slices.Sort(got)
	want := []string{basic, "ops@gw.example", "short1", "X-Gateway: Bearer s12", "c=devin-session-token$s12"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("AccountSecrets = %q\nwant %q", got, want)
	}
	for in, want := range map[string]string{
		"invalid short1":                        "invalid [redacted]",
		"the gateway refuses Bearer, use a key": "the [redacted] refuses [redacted], use a key",
		"no devin session token":                "no [redacted] [redacted] [redacted]",
		"user ops@gw.example is not known":      "user [redacted] is not known",
	} {
		if got := UpstreamText(in, AccountSecrets(a)...); got != want {
			t.Errorf("UpstreamText(%q) = %q, want %q", in, got, want)
		}
	}
}
