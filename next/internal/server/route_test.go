package server

import (
	"encoding/json"
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

	// Gemini clients send the key in x-goog-api-key or the key query parameter.
	goog := httptest.NewRequest("POST", "/v1beta/models/m:generateContent", nil)
	goog.Header.Set("X-Goog-Api-Key", "key-a")
	query := httptest.NewRequest("POST", "/v1beta/models/m:generateContent?key=key-a", nil)
	if tenant(goog) != tenant(bearer) || tenant(query) != tenant(bearer) {
		t.Errorf("x-goog-api-key tenant %q, ?key= tenant %q, bearer tenant %q; want all the same", tenant(goog), tenant(query), tenant(bearer))
	}
}

// A request without a session header is bound by its body's prompt_cache_key, read as
// json.Unmarshal reads it into a string field: any letter case, the last one, escapes decoded,
// and none when one of them is not a string or the body is not a JSON object.
func TestSessionKeyReadsPromptCacheKeyAsUnmarshalDoes(t *testing.T) {
	for body, want := range map[string]string{
		"{\"model\":\"m\",\"prompt_cache_key\":\"k1\",\"messages\":[]}":        "k1",
		"{\"messages\":[{\"content\":\"x\"}], \"prompt_cache_key\" : \"k2\" }": "k2",
		"{\"PROMPT_Cache_Key\":\"k3\"}":                                        "k3",
		"{\"prompt_cache_key\":\"a\",\"PROMPT_CACHE_KEY\":\"b\"}":              "b",
		"{\"prompt_cache_key\":\"a\",\"prompt_cache_key\":null}":               "a",
		"{\"prompt_\\u0063ache_key\":\"esc\\u00e9\\n\"}":                       "escé\n",
		"{\"prompt_cache_key\":\"a\",\"prompt_cache_key\":5}":                  "",
		"{\"prompt_cache_key\":\"\"}":                                          "",
		"{\"metadata\":{\"prompt_cache_key\":\"nested\"}}":                     "",
		"{\"prompt_cache_key\":\"k\",\"model\":}":                              "",
		"[\"prompt_cache_key\",\"k\"]":                                         "",
		"null":                                                                 "",
		"{\"prompt_cache_key\":\"bad \xff utf-8\"}":                            "bad � utf-8",
		"{\"prompt_cache_key\":\"\\ud83d\\ude00\"}":                            "😀",
	} {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		got := sessionKey(r, []byte(body))
		var b struct {
			PromptCacheKey string "json:\"prompt_cache_key\""
		}
		decoded := ""
		if json.Unmarshal([]byte(body), &b) == nil && b.PromptCacheKey != "" {
			decoded = "prompt_cache_key:" + b.PromptCacheKey
		}
		if want != "" {
			want = "prompt_cache_key:" + want
		}
		if got != want || got != decoded {
			t.Errorf("sessionKey(%s) = %q, want %q (json.Unmarshal reads %q)", body, got, want, decoded)
		}
	}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("Session-Id", "s1")
	if got := sessionKey(r, []byte("{\"prompt_cache_key\":\"k\"}")); got != "Session-Id:s1" {
		t.Errorf("a session header gave %q, want it to win over the body", got)
	}
}
