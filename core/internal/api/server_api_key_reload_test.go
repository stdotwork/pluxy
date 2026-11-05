package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// apiKeyTransport describes one way a client can present its API key.
type apiKeyTransport struct {
	name    string
	request func(key string) *http.Request
}

func modelsRequest(path string, header string, valuePrefix string) func(string) *http.Request {
	return func(key string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if header != "" {
			req.Header.Set(header, valuePrefix+key)
		}
		return req
	}
}

var apiKeyTransports = []apiKeyTransport{
	{"Authorization header", modelsRequest("/v1/models", "Authorization", "Bearer ")},
	{"X-Goog-Api-Key header", modelsRequest("/v1/models", "X-Goog-Api-Key", "")},
	{"X-Api-Key header", modelsRequest("/v1/models", "X-Api-Key", "")},
	{"key query parameter", func(key string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v1/models?key="+key, nil)
	}},
}

func apiKeyStatus(server *Server, tr apiKeyTransport, key string) int {
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, tr.request(key))
	return rr.Code
}

// TestUpdateClientsRevokesRemovedAPIKey checks that a client API key removed by a hot reload
// stops working immediately on every transport, and that its replacement works on all of them.
func TestUpdateClientsRevokesRemovedAPIKey(t *testing.T) {
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"old-key"}}}
	server := newTestServerWithConfig(t, cfg)

	// Before the reload the old key must be accepted on every transport; otherwise the
	// rejections below would prove nothing about the reload.
	for _, tr := range apiKeyTransports {
		if got := apiKeyStatus(server, tr, "old-key"); got != http.StatusOK {
			t.Fatalf("old key via %s before reload = %d, want %d", tr.name, got, http.StatusOK)
		}
	}
	if got := apiKeyStatus(server, apiKeyTransports[0], "new-key"); got != http.StatusUnauthorized {
		t.Fatalf("new key before reload = %d, want %d", got, http.StatusUnauthorized)
	}

	next := *cfg
	next.APIKeys = []string{"new-key"}
	server.UpdateClients(&next)

	for _, tr := range apiKeyTransports {
		t.Run(tr.name, func(t *testing.T) {
			if got := apiKeyStatus(server, tr, "old-key"); got != http.StatusUnauthorized {
				t.Errorf("removed key after reload = %d, want %d", got, http.StatusUnauthorized)
			}
			if got := apiKeyStatus(server, tr, "new-key"); got != http.StatusOK {
				t.Errorf("new key after reload = %d, want %d", got, http.StatusOK)
			}
		})
	}
}
