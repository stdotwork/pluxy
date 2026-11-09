package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestExampleClientKeys(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want []string
	}{
		{nil, nil},
		{[]string{"sk-mine"}, nil},
		{[]string{"your-api-key-1"}, []string{"your-api-key-1"}},
		{[]string{"sk-mine", " your-api-key-3\n", "your-api-key-2", "your-api-key-3"}, []string{"your-api-key-3", "your-api-key-2"}},
		{[]string{"your-api-key-4", "your-api-key"}, nil}, // only the example config's three
	} {
		c := &Config{ClientKeys: tc.keys}
		if got := c.ExampleClientKeys(); !slices.Equal(got, tc.want) {
			t.Errorf("keys %q: ExampleClientKeys = %q, want %q", tc.keys, got, tc.want)
		}
	}
}

// Today's core refuses every proxy request while a client key is one of the example config's
// public ones (its "example API-key safe mode"): the keys are public, so whoever configured them
// has not set up access. A request that does not carry a configured key is refused as ever, so
// the state is shown only to those who hold a key.
func TestExampleClientKeysRefuseEveryRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		key  string
		want int
	}{
		{"an example key", []string{"your-api-key-1"}, "your-api-key-1", http.StatusForbidden},
		{"a real key beside an example one", []string{"sk-mine", "your-api-key-2"}, "sk-mine", http.StatusForbidden},
		{"an example key with spaces around it", []string{" your-api-key-3 "}, " your-api-key-3 ", http.StatusForbidden},
		{"a key that is not configured", []string{"your-api-key-1"}, "sk-other", http.StatusUnauthorized},
		{"no key", []string{"your-api-key-1"}, "", http.StatusUnauthorized},
		{"real keys only", []string{"sk-mine"}, "sk-mine", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testHandler(t, &Config{ClientKeys: tc.keys}, hello)
			for _, req := range []struct {
				method, path, body string
				refusalOnly        bool // the test handler serves no such route, so only a refusal is checked
			}{
				{"POST", "/v1/chat/completions", chatBody, false},
				{"POST", "/v1/messages", `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, false},
				{"POST", "/v1beta/models/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, false},
				{"GET", "/v1/models", "", false},
				{"GET", "/v1beta/models", "", false},
				{"POST", "/v1/messages/count_tokens", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, true},
				{"POST", "/v1beta/models/m:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, true},
				{"GET", "/v1/responses", "", true}, // the WebSocket upgrade, refused before it is accepted
			} {
				if req.refusalOnly && tc.want == http.StatusOK {
					continue
				}
				r := httptest.NewRequest(req.method, req.path, strings.NewReader(req.body))
				if tc.key != "" {
					r.Header.Set("Authorization", "Bearer "+tc.key)
				}
				if req.path == "/v1/responses" {
					r.Header.Set("Connection", "Upgrade")
					r.Header.Set("Upgrade", "websocket")
					r.Header.Set("Sec-WebSocket-Version", "13")
					r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.want {
					t.Errorf("%s %s: status %d, want %d: %s", req.method, req.path, w.Code, tc.want, w.Body)
				}
				if tc.want != http.StatusForbidden {
					continue
				}
				// Every format says why; the OpenAI shape also carries the code.
				if !strings.Contains(w.Body.String(), "client_keys holds the example config's key") {
					t.Errorf("%s %s: body %s does not say why", req.method, req.path, w.Body)
				}
				if strings.HasPrefix(req.path, "/v1/chat") && !strings.Contains(w.Body.String(), "unsafe_example_api_key") {
					t.Errorf("%s %s: body %s lacks the code unsafe_example_api_key", req.method, req.path, w.Body)
				}
			}
		})
	}
}

// Core starts with an example key and says so at every load, through the config's notices.
func TestParseConfigNamesExampleClientKeys(t *testing.T) {
	parse := func(keys string) []string {
		t.Helper()
		c, err := ParseConfig([]byte(`{"listen": "127.0.0.1:0", "client_keys": `+keys+`, "management_key": "m",
			"extension": {}}`), "/srv/pluxy/config.json")
		if err != nil {
			t.Fatal(err)
		}
		return c.Notices
	}
	if n := parse(`["sk-mine"]`); len(n) != 0 {
		t.Errorf("notices for real keys: %q", n)
	}
	n := parse(`["sk-mine", "your-api-key-1", "your-api-key-3"]`)
	if len(n) != 1 || !strings.Contains(n[0], "your-api-key-1, your-api-key-3") || !strings.Contains(n[0], "unsafe_example_api_key") {
		t.Errorf("notices for example keys: %q", n)
	}
}
