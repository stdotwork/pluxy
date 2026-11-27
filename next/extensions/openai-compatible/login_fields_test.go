package main

import "testing"

// A new account needs its base URL: login refuses an empty one. Sign-in again keeps the stored
// one, which the form handles, so the field itself is not optional.
func TestAPIKeyLoginBaseURLIsRequired(t *testing.T) {
	inputs := map[string]bool{}
	for _, in := range loginMethods[0].Inputs {
		inputs[in.Name] = in.Optional
	}
	if optional, ok := inputs["base_url"]; !ok || optional {
		t.Fatalf("base_url optional = %v (present %v), want present and required", optional, ok)
	}
	if optional, ok := inputs["wire_api"]; !ok || !optional {
		t.Fatalf("wire_api optional = %v (present %v), want present and optional", optional, ok)
	}
	if optional, ok := inputs["quota"]; !ok || !optional {
		t.Fatalf("quota optional = %v (present %v), want present and optional", optional, ok)
	}
}

// The form's labels explain each field in plain words and keep the accepted values exact, without
// config jargon or Gemini-specific URL paths on an OpenAI-compatible form.
func TestAPIKeyLoginFieldLabels(t *testing.T) {
	want := map[string]string{
		"base_url": "Service address (the API URL, including /v1 for most services)",
		"api_key":  "API key",
		"wire_api": "How pluxy talks to it. Leave blank for most services (uses chat). Other values: responses, messages, gemini.",
		"quota":    "How pluxy checks your remaining balance. Leave blank for most services (detected from the address). Values: openrouter, deepseek, none.",
	}
	got := map[string]string{}
	for _, in := range loginMethods[0].Inputs {
		got[in.Name] = in.Label
	}
	for name, wantLabel := range want {
		if got[name] != wantLabel {
			t.Errorf("input %q label = %q, want %q", name, got[name], wantLabel)
		}
	}
}
