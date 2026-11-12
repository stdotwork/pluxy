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
}
