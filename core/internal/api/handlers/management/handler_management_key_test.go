package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const (
	testRemoteKey     = "remote-management-key"
	testLocalPassword = "local-runtime-password"
	testRemoteIP      = "203.0.113.9"
	testLocalIP       = "127.0.0.1"
)

// newKeyGateHandler builds a Handler whose config holds a bcrypt hash of testRemoteKey,
// the way a real config.yaml stores remote-management.secret-key.
func newKeyGateHandler(t *testing.T, allowRemote bool) *Handler {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testRemoteKey), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = string(hash)
	cfg.RemoteManagement.AllowRemote = allowRemote
	return &Handler{
		cfg:            cfg,
		failedAttempts: make(map[string]*attemptInfo),
		localPassword:  testLocalPassword,
	}
}

func TestAuthenticateManagementKey_Gates(t *testing.T) {
	tests := []struct {
		name        string
		allowRemote bool
		clientIP    string
		local       bool
		key         string
		wantAllowed bool
		wantStatus  int
		wantMsg     string
	}{
		{
			name:        "remote client with remote management disabled is rejected even with the correct key",
			allowRemote: false, clientIP: testRemoteIP, local: false, key: testRemoteKey,
			wantStatus: http.StatusForbidden, wantMsg: "remote management disabled",
		},
		{
			name:        "remote client with remote management enabled is allowed with the bcrypt key",
			allowRemote: true, clientIP: testRemoteIP, local: false, key: testRemoteKey,
			wantAllowed: true,
		},
		{
			name:        "remote client cannot use the local password",
			allowRemote: true, clientIP: testRemoteIP, local: false, key: testLocalPassword,
			wantStatus: http.StatusUnauthorized, wantMsg: "invalid management key",
		},
		{
			name:        "local client is allowed with the local password",
			allowRemote: false, clientIP: testLocalIP, local: true, key: testLocalPassword,
			wantAllowed: true,
		},
		{
			name:        "local client is allowed with the bcrypt key",
			allowRemote: false, clientIP: testLocalIP, local: true, key: testRemoteKey,
			wantAllowed: true,
		},
		{
			name:        "local client with a wrong key is rejected",
			allowRemote: false, clientIP: testLocalIP, local: true, key: "wrong-key",
			wantStatus: http.StatusUnauthorized, wantMsg: "invalid management key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newKeyGateHandler(t, tt.allowRemote)

			allowed, status, msg := h.AuthenticateManagementKey(tt.clientIP, tt.local, tt.key)

			if allowed != tt.wantAllowed || status != tt.wantStatus || msg != tt.wantMsg {
				t.Fatalf("AuthenticateManagementKey() = (%v, %d, %q), want (%v, %d, %q)",
					allowed, status, msg, tt.wantAllowed, tt.wantStatus, tt.wantMsg)
			}
		})
	}
}

func TestAuthenticateManagementKey_RemoteManagementKeyNotSet(t *testing.T) {
	for _, local := range []bool{true, false} {
		name := "remote client"
		ip := testRemoteIP
		if local {
			name = "local client"
			ip = testLocalIP
		}
		t.Run(name, func(t *testing.T) {
			// Neither a config hash nor an environment secret: no key can ever match, so the
			// request must be refused outright rather than counted as a failed attempt.
			h := &Handler{
				cfg:            &config.Config{},
				failedAttempts: make(map[string]*attemptInfo),
				localPassword:  testLocalPassword,
			}
			h.cfg.RemoteManagement.AllowRemote = true

			allowed, status, msg := h.AuthenticateManagementKey(ip, local, testLocalPassword)

			if allowed || status != http.StatusForbidden || msg != "remote management key not set" {
				t.Fatalf("AuthenticateManagementKey() = (%v, %d, %q), want (false, %d, %q)",
					allowed, status, msg, http.StatusForbidden, "remote management key not set")
			}
			if len(h.failedAttempts) != 0 {
				t.Fatalf("a missing key must not be recorded as a failed attempt, got %d entries", len(h.failedAttempts))
			}
		})
	}
}

func TestAuthenticateManagementKey_ExpiredBanIsReset(t *testing.T) {
	t.Run("correct key is allowed and the ban state is cleared", func(t *testing.T) {
		h := newKeyGateHandler(t, false)
		h.failedAttempts[testLocalIP] = &attemptInfo{count: 3, blockedUntil: time.Now().Add(-time.Second)}

		allowed, status, msg := h.AuthenticateManagementKey(testLocalIP, true, testLocalPassword)

		if !allowed || status != 0 || msg != "" {
			t.Fatalf("AuthenticateManagementKey() = (%v, %d, %q), want (true, 0, \"\")", allowed, status, msg)
		}
		ai := h.failedAttempts[testLocalIP]
		if ai.count != 0 || !ai.blockedUntil.IsZero() {
			t.Fatalf("ban state not cleared: count=%d blockedUntil=%v", ai.count, ai.blockedUntil)
		}
	})

	t.Run("wrong key is a plain 401 that starts a fresh failure count", func(t *testing.T) {
		h := newKeyGateHandler(t, false)
		h.failedAttempts[testLocalIP] = &attemptInfo{count: 4, blockedUntil: time.Now().Add(-time.Second)}

		allowed, status, msg := h.AuthenticateManagementKey(testLocalIP, true, "wrong-key")

		if allowed || status != http.StatusUnauthorized || msg != "invalid management key" {
			t.Fatalf("AuthenticateManagementKey() = (%v, %d, %q), want (false, %d, %q)",
				allowed, status, msg, http.StatusUnauthorized, "invalid management key")
		}
		// The old count of 4 must not carry over: one new failure leaves a count of 1.
		if got := h.failedAttempts[testLocalIP].count; got != 1 {
			t.Fatalf("failure count after expired ban = %d, want 1", got)
		}
	})
}

func TestMiddleware_RejectsNonLocalClientWithoutTrustedProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Remote management is disabled and the request carries the correct key, so only the
	// remote gate can produce the 403.
	h := newKeyGateHandler(t, false)

	engine := gin.New()
	if err := engine.SetTrustedProxies(nil); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}
	engine.GET("/v0/management/config", h.Middleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "key in X-Management-Key", headers: map[string]string{"X-Management-Key": testRemoteKey}},
		{name: "key as bearer token", headers: map[string]string{"Authorization": "Bearer " + testRemoteKey}},
		// Without trusted proxies a forwarded header must not turn a remote client into a local one.
		{name: "spoofed X-Forwarded-For loopback", headers: map[string]string{
			"X-Management-Key": testRemoteKey,
			"X-Forwarded-For":  testLocalIP,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
			req.RemoteAddr = testRemoteIP + ":43210"
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()

			engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
			}
		})
	}
}
