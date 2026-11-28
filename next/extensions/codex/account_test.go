package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

func idTokenWithAuth(auth map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(map[string]any{
		"https://api.openai.com/auth": auth,
	}) + ".x"
}

func TestSubscriptionPlan(t *testing.T) {
	for _, tc := range []struct {
		name string
		cred *protocol.Credential
		want string
	}{
		{"plus", subscriptionWith(idTokenWithPlan("plus")), "plus"},
		{"free", subscriptionWith(idTokenWithPlan("free")), "free"},
		{"no plan claim", subscriptionWith(idTokenWithAuth(map[string]any{"chatgpt_account_id": "acct"})), "free"},
		{"null plan claim", subscriptionWith(idTokenWithAuth(map[string]any{"chatgpt_account_id": "acct", "chatgpt_plan_type": nil})), "free"},
		{"blank plan claim", subscriptionWith(idTokenWithPlan("  ")), "free"},
		{"ID token that does not parse", subscriptionWith("not-a-jwt"), "free"},
		{"no ID token", subscriptionWith(""), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := accountOf(tc.cred)
			if err != nil {
				t.Fatal(err)
			}
			if a.planType != tc.want {
				t.Errorf("plan %q, want %q", a.planType, tc.want)
			}
		})
	}
}

func TestImageToolOffered(t *testing.T) {
	for _, tc := range []struct {
		name string
		cred *protocol.Credential
		want bool
	}{
		{"plus", subscriptionWith(idTokenWithPlan("plus")), true},
		{"free", subscriptionWith(idTokenWithPlan("free")), false},
		{"free in capitals", subscriptionWith(idTokenWithPlan("Free")), false},
		{"no plan claim", subscriptionWith(idTokenWithAuth(map[string]any{"chatgpt_account_id": "acct"})), false},
		{"ID token that does not parse", subscriptionWith("not-a-jwt"), false},
		{"no ID token", subscriptionWith(""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := accountOf(tc.cred)
			if err != nil {
				t.Fatal(err)
			}
			sh := shaper{m: map[string]json.RawMessage{}, model: "gpt-6.1-terra", a: a}
			sh.tools()
			if got := strings.Contains(string(sh.m["tools"]), "image_generation"); got != tc.want {
				t.Errorf("image_generation offered %v, want %v (tools %s)", got, tc.want, sh.m["tools"])
			}
		})
	}
}
