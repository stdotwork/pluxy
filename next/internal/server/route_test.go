package server

import (
	"net/http/httptest"
	"testing"
)

// A client key sent as x-api-key (as Anthropic clients send it) names the same tenant as the
// same key sent as a bearer token, and different keys name different tenants, so session
// bindings stay per client key whichever header carries it.
func TestTenantFollowsTheClientKey(t *testing.T) {
	bearer := httptest.NewRequest("POST", "/v1/messages", nil)
	bearer.Header.Set("Authorization", "Bearer key-a")
	apiKey := httptest.NewRequest("POST", "/v1/messages", nil)
	apiKey.Header.Set("X-Api-Key", "key-a")
	other := httptest.NewRequest("POST", "/v1/messages", nil)
	other.Header.Set("X-Api-Key", "key-b")
	none := httptest.NewRequest("POST", "/v1/messages", nil)

	if tenant(apiKey) == "" || tenant(apiKey) != tenant(bearer) {
		t.Errorf("x-api-key tenant %q, bearer tenant %q; want the same, not empty", tenant(apiKey), tenant(bearer))
	}
	if tenant(other) == tenant(apiKey) {
		t.Errorf("keys key-a and key-b share tenant %q", tenant(other))
	}
	if tenant(none) != "" {
		t.Errorf("no key gave tenant %q, want empty", tenant(none))
	}
}
