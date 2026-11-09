package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// idTokenWithAuth is an unsigned ID token whose ChatGPT auth claim is auth, for the cases a plan
// claim is missing or null.
func idTokenWithAuth(auth map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(map[string]any{
		"https://api.openai.com/auth": auth,
	}) + ".x"
}

// TestSubscriptionPlan checks the plan an account carries, as today's core reads it from the ID
// token (core/internal/watcher/synthesizer/file.go): an ID token with no plan, or one that does
// not parse, is the free plan, and a credential with no ID token has no plan to read.
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

// TestImageToolOffered checks the hosted image_generation tool for each kind of account: a free
// plan gets none, as in today's core (core/internal/runtime/executor/codex_executor_request.go),
// and an account with no ID token keeps it.
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
