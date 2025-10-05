package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	geminiAuth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	iflowauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qwen"
	// legacy client removed
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var (
	oauthStatus = make(map[string]string)
)

var lastRefreshKeys = []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"}

const (
	anthropicCallbackPort = 54545
	geminiCallbackPort    = 8085
	codexCallbackPort     = 1455
)

type callbackForwarder struct {
	provider string
	server   *http.Server
	done     chan struct{}
}

var (
	callbackForwardersMu sync.Mutex
	callbackForwarders   = make(map[int]*callbackForwarder)
)

func extractLastRefreshTimestamp(meta map[string]any) (time.Time, bool) {
	if len(meta) == 0 {
		return time.Time{}, false
	}
	for _, key := range lastRefreshKeys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseLastRefreshValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func parseLastRefreshValue(v any) (time.Time, bool) {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC(), true
		}
	case float64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case int64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(val, 0).UTC(), true
	case int:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case json.Number:
		if i, err := val.Int64(); err == nil && i > 0 {
			return time.Unix(i, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func isWebUIRequest(c *gin.Context) bool {
	raw := strings.TrimSpace(c.Query("is_webui"))
	if raw == "" {
		return false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func startCallbackForwarder(port int, provider, targetBase string) (*callbackForwarder, error) {
	callbackForwardersMu.Lock()
	prev := callbackForwarders[port]
	if prev != nil {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	if prev != nil {
		stopForwarderInstance(port, prev)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := targetBase
		if raw := r.URL.RawQuery; raw != "" {
			if strings.Contains(target, "?") {
				target = target + "&" + raw
			} else {
				target = target + "?" + raw
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	done := make(chan struct{})

	go func() {
		if errServe := srv.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.WithError(errServe).Warnf("callback forwarder for %s stopped unexpectedly", provider)
		}
		close(done)
	}()

	forwarder := &callbackForwarder{
		provider: provider,
		server:   srv,
		done:     done,
	}

	callbackForwardersMu.Lock()
	callbackForwarders[port] = forwarder
	callbackForwardersMu.Unlock()

	log.Infof("callback forwarder for %s listening on %s", provider, addr)

	return forwarder, nil
}

func stopCallbackForwarder(port int) {
	callbackForwardersMu.Lock()
	forwarder := callbackForwarders[port]
	if forwarder != nil {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	stopForwarderInstance(port, forwarder)
}

func stopForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil || forwarder.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := forwarder.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.WithError(err).Warnf("failed to shut down callback forwarder on port %d", port)
	}

	select {
	case <-forwarder.done:
	case <-time.After(2 * time.Second):
	}

	log.Infof("callback forwarder on port %d stopped", port)
}

func (h *Handler) managementCallbackURL(path string) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", h.cfg.Port, path), nil
}

// List auth files
func (h *Handler) ListAuthFiles(c *gin.Context) {
	entries, err := os.ReadDir(h.cfg.AuthDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
		return
	}
	files := make([]gin.H, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if info, errInfo := e.Info(); errInfo == nil {
			fileData := gin.H{"name": name, "size": info.Size(), "modtime": info.ModTime()}

			// Read file to get type field
			full := filepath.Join(h.cfg.AuthDir, name)
			if data, errRead := os.ReadFile(full); errRead == nil {
				typeValue := gjson.GetBytes(data, "type").String()
				fileData["type"] = typeValue
			}

			files = append(files, fileData)
		}
	}
	c.JSON(200, gin.H{"files": files})
}

// Download single auth file by name
func (h *Handler) DownloadAuthFile(c *gin.Context) {
	name := c.Query("name")
	if name == "" || strings.Contains(name, string(os.PathSeparator)) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	full := filepath.Join(h.cfg.AuthDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "file not found"})
		} else {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
		}
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(200, "application/json", data)
}

// Upload auth file: multipart or raw JSON with ?name=
func (h *Handler) UploadAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	if file, err := c.FormFile("file"); err == nil && file != nil {
		name := filepath.Base(file.Filename)
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			c.JSON(400, gin.H{"error": "file must be .json"})
			return
		}
		dst := filepath.Join(h.cfg.AuthDir, name)
		if !filepath.IsAbs(dst) {
			if abs, errAbs := filepath.Abs(dst); errAbs == nil {
				dst = abs
			}
		}
		if errSave := c.SaveUploadedFile(file, dst); errSave != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to save file: %v", errSave)})
			return
		}
		data, errRead := os.ReadFile(dst)
		if errRead != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read saved file: %v", errRead)})
			return
		}
		if errReg := h.registerAuthFromFile(ctx, dst, data); errReg != nil {
			c.JSON(500, gin.H{"error": errReg.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
		return
	}
	name := c.Query("name")
	if name == "" || strings.Contains(name, string(os.PathSeparator)) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	if errWrite := os.WriteFile(dst, data, 0o600); errWrite != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to write file: %v", errWrite)})
		return
	}
	if err = h.registerAuthFromFile(ctx, dst, data); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

// Delete auth files: single by name or all
func (h *Handler) DeleteAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	if all := c.Query("all"); all == "true" || all == "1" || all == "*" {
		entries, err := os.ReadDir(h.cfg.AuthDir)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
			return
		}
		deleted := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			full := filepath.Join(h.cfg.AuthDir, name)
			if !filepath.IsAbs(full) {
				if abs, errAbs := filepath.Abs(full); errAbs == nil {
					full = abs
				}
			}
			if err = os.Remove(full); err == nil {
				deleted++
				h.disableAuth(ctx, full)
			}
		}
		c.JSON(200, gin.H{"status": "ok", "deleted": deleted})
		return
	}
	name := c.Query("name")
	if name == "" || strings.Contains(name, string(os.PathSeparator)) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	full := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(full) {
		if abs, errAbs := filepath.Abs(full); errAbs == nil {
			full = abs
		}
	}
	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "file not found"})
		} else {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to remove file: %v", err)})
		}
		return
	}
	h.disableAuth(ctx, full)
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Handler) registerAuthFromFile(ctx context.Context, path string, data []byte) error {
	if h.authManager == nil {
		return nil
	}
	if path == "" {
		return fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read auth file: %w", err)
		}
	}
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("invalid auth file: %w", err)
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && email != "" {
		label = email
	}
	lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)

	attr := map[string]string{
		"path":   path,
		"source": path,
	}
	auth := &coreauth.Auth{
		ID:         path,
		Provider:   provider,
		Label:      label,
		Status:     coreauth.StatusActive,
		Attributes: attr,
		Metadata:   metadata,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if hasLastRefresh {
		auth.LastRefreshedAt = lastRefresh
	}
	if existing, ok := h.authManager.GetByID(path); ok {
		auth.CreatedAt = existing.CreatedAt
		if !hasLastRefresh {
			auth.LastRefreshedAt = existing.LastRefreshedAt
		}
		auth.NextRefreshAfter = existing.NextRefreshAfter
		auth.Runtime = existing.Runtime
		_, err := h.authManager.Update(ctx, auth)
		return err
	}
	_, err := h.authManager.Register(ctx, auth)
	return err
}

func (h *Handler) disableAuth(ctx context.Context, id string) {
	if h.authManager == nil || id == "" {
		return
	}
	if auth, ok := h.authManager.GetByID(id); ok {
		auth.Disabled = true
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "removed via management API"
		auth.UpdatedAt = time.Now()
		_, _ = h.authManager.Update(ctx, auth)
	}
}

func (h *Handler) saveTokenRecord(ctx context.Context, record *coreauth.Auth) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}
	store := h.tokenStore
	if store == nil {
		store = sdkAuth.GetTokenStore()
		h.tokenStore = store
	}
	if h.cfg != nil {
		if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(h.cfg.AuthDir)
		}
	}
	return store.Save(ctx, record)
}

func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := context.Background()

	fmt.Println("Initializing Claude authentication...")

	// Generate PKCE codes
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		log.Fatalf("Failed to generate PKCE codes: %v", err)
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Fatalf("Failed to generate state parameter: %v", err)
		return
	}

	// Initialize Claude auth service
	anthropicAuth := claude.NewClaudeAuth(h.cfg)

	// Generate authorization URL (then override redirect_uri to reuse server port)
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Fatalf("Failed to generate authorization URL: %v", err)
		return
	}

	isWebUI := isWebUIRequest(c)
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/anthropic/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute anthropic callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		if _, errStart := startCallbackForwarder(anthropicCallbackPort, "anthropic", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start anthropic callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarder(anthropicCallbackPort)
		}

		// Helper: wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-anthropic-%s.oauth", state))
		waitForFile := func(path string, timeout time.Duration) (map[string]string, error) {
			deadline := time.Now().Add(timeout)
			for {
				if time.Now().After(deadline) {
					oauthStatus[state] = "Timeout waiting for OAuth callback"
					return nil, fmt.Errorf("timeout waiting for OAuth callback")
				}
				data, errRead := os.ReadFile(path)
				if errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(path)
					return m, nil
				}
				time.Sleep(500 * time.Millisecond)
			}
		}

		fmt.Println("Waiting for authentication callback...")
		// Wait up to 5 minutes
		resultMap, errWait := waitForFile(waitFile, 5*time.Minute)
		if errWait != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, errWait)
			log.Error(claude.GetUserFriendlyMessage(authErr))
			return
		}
		if errStr := resultMap["error"]; errStr != "" {
			oauthErr := claude.NewOAuthError(errStr, "", http.StatusBadRequest)
			log.Error(claude.GetUserFriendlyMessage(oauthErr))
			oauthStatus[state] = "Bad request"
			return
		}
		if resultMap["state"] != state {
			authErr := claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, resultMap["state"]))
			log.Error(claude.GetUserFriendlyMessage(authErr))
			oauthStatus[state] = "State code error"
			return
		}

		// Parse code (Claude may append state after '#')
		rawCode := resultMap["code"]
		code := strings.Split(rawCode, "#")[0]

		// Exchange code for tokens (replicate logic using updated redirect_uri)
		// Extract client_id from the modified auth URL
		clientID := ""
		if u2, errP := url.Parse(authURL); errP == nil {
			clientID = u2.Query().Get("client_id")
		}
		// Build request
		bodyMap := map[string]any{
			"code":          code,
			"state":         state,
			"grant_type":    "authorization_code",
			"client_id":     clientID,
			"redirect_uri":  "http://localhost:54545/callback",
			"code_verifier": pkceCodes.CodeVerifier,
		}
		bodyJSON, _ := json.Marshal(bodyMap)

		httpClient := util.SetProxy(&h.cfg.SDKConfig, &http.Client{})
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://console.anthropic.com/v1/oauth/token", strings.NewReader(string(bodyJSON)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, errDo)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			oauthStatus[state] = "Failed to exchange authorization code for tokens"
			return
		}
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Errorf("failed to close response body: %v", errClose)
			}
		}()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			log.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(respBody))
			oauthStatus[state] = fmt.Sprintf("token exchange failed with status %d", resp.StatusCode)
			return
		}
		var tResp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
			Account      struct {
				EmailAddress string `json:"email_address"`
			} `json:"account"`
		}
		if errU := json.Unmarshal(respBody, &tResp); errU != nil {
			log.Errorf("failed to parse token response: %v", errU)
			oauthStatus[state] = "Failed to parse token response"
			return
		}
		bundle := &claude.ClaudeAuthBundle{
			TokenData: claude.ClaudeTokenData{
				AccessToken:  tResp.AccessToken,
				RefreshToken: tResp.RefreshToken,
				Email:        tResp.Account.EmailAddress,
				Expire:       time.Now().Add(time.Duration(tResp.ExpiresIn) * time.Second).Format(time.RFC3339),
			},
			LastRefresh: time.Now().Format(time.RFC3339),
		}

		// Create token storage
		tokenStorage := anthropicAuth.CreateTokenStorage(bundle)
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Provider: "claude",
			FileName: fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: map[string]any{"email": tokenStorage.Email},
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Fatalf("Failed to save authentication tokens: %v", errSave)
			oauthStatus[state] = "Failed to save authentication tokens"
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Claude services through this CLI")
		delete(oauthStatus, state)
	}()

	oauthStatus[state] = ""
	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGeminiCLIToken(c *gin.Context) {
	ctx := context.Background()

	// Optional project ID from query
	projectID := c.Query("project_id")

	fmt.Println("Initializing Google authentication...")

	// OAuth2 configuration (mirrors internal/auth/gemini)
	conf := &oauth2.Config{
		ClientID:     "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com",
		ClientSecret: "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",
		RedirectURL:  "http://localhost:8085/oauth2callback",
		Scopes: []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Endpoint: google.Endpoint,
	}

	// Build authorization URL and return it immediately
	state := fmt.Sprintf("gem-%d", time.Now().UnixNano())
	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	isWebUI := isWebUIRequest(c)
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/google/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute gemini callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		if _, errStart := startCallbackForwarder(geminiCallbackPort, "gemini", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start gemini callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarder(geminiCallbackPort)
		}

		// Wait for callback file written by server route
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-gemini-%s.oauth", state))
		fmt.Println("Waiting for authentication callback...")
		deadline := time.Now().Add(5 * time.Minute)
		var authCode string
		for {
			if time.Now().After(deadline) {
				log.Error("oauth flow timed out")
				oauthStatus[state] = "OAuth flow timed out"
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					log.Errorf("Authentication failed: %s", errStr)
					oauthStatus[state] = "Authentication failed"
					return
				}
				authCode = m["code"]
				if authCode == "" {
					log.Errorf("Authentication failed: code not found")
					oauthStatus[state] = "Authentication failed: code not found"
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		// Exchange authorization code for token
		token, err := conf.Exchange(ctx, authCode)
		if err != nil {
			log.Errorf("Failed to exchange token: %v", err)
			oauthStatus[state] = "Failed to exchange token"
			return
		}

		// Create token storage (mirrors internal/auth/gemini createTokenStorage)
		httpClient := conf.Client(ctx, token)
		req, errNewRequest := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
		if errNewRequest != nil {
			log.Errorf("Could not get user info: %v", errNewRequest)
			oauthStatus[state] = "Could not get user info"
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			log.Errorf("Failed to execute request: %v", errDo)
			oauthStatus[state] = "Failed to execute request"
			return
		}
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Printf("warn: failed to close response body: %v", errClose)
			}
		}()

		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Errorf("Get user info request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
			oauthStatus[state] = fmt.Sprintf("Get user info request failed with status %d", resp.StatusCode)
			return
		}

		email := gjson.GetBytes(bodyBytes, "email").String()
		if email != "" {
			fmt.Printf("Authenticated user email: %s\n", email)
		} else {
			fmt.Println("Failed to get user email from token")
			oauthStatus[state] = "Failed to get user email from token"
		}

		// Marshal/unmarshal oauth2.Token to generic map and enrich fields
		var ifToken map[string]any
		jsonData, _ := json.Marshal(token)
		if errUnmarshal := json.Unmarshal(jsonData, &ifToken); errUnmarshal != nil {
			log.Errorf("Failed to unmarshal token: %v", errUnmarshal)
			oauthStatus[state] = "Failed to unmarshal token"
			return
		}

		ifToken["token_uri"] = "https://oauth2.googleapis.com/token"
		ifToken["client_id"] = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
		ifToken["client_secret"] = "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"
		ifToken["scopes"] = []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		}
		ifToken["universe_domain"] = "googleapis.com"

		ts := geminiAuth.GeminiTokenStorage{
			Token:     ifToken,
			ProjectID: projectID,
			Email:     email,
		}

		// Initialize authenticated HTTP client via GeminiAuth to honor proxy settings
		gemAuth := geminiAuth.NewGeminiAuth()
		_, errGetClient := gemAuth.GetAuthenticatedClient(ctx, &ts, h.cfg, true)
		if errGetClient != nil {
			log.Fatalf("failed to get authenticated client: %v", errGetClient)
			oauthStatus[state] = "Failed to get authenticated client"
			return
		}
		fmt.Println("Authentication successful.")

		record := &coreauth.Auth{
			ID:       fmt.Sprintf("gemini-%s.json", ts.Email),
			Provider: "gemini",
			FileName: fmt.Sprintf("gemini-%s.json", ts.Email),
			Storage:  &ts,
			Metadata: map[string]any{
				"email":      ts.Email,
				"project_id": ts.ProjectID,
			},
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Fatalf("Failed to save token to file: %v", errSave)
			oauthStatus[state] = "Failed to save token to file"
			return
		}

		delete(oauthStatus, state)
		fmt.Printf("You can now use Gemini CLI services through this CLI; token saved to %s\n", savedPath)
	}()

	oauthStatus[state] = ""
	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) CreateGeminiWebToken(c *gin.Context) {
	ctx := c.Request.Context()

	var payload struct {
		Secure1PSID   string `json:"secure_1psid"`
		Secure1PSIDTS string `json:"secure_1psidts"`
		Label         string `json:"label"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	payload.Secure1PSID = strings.TrimSpace(payload.Secure1PSID)
	payload.Secure1PSIDTS = strings.TrimSpace(payload.Secure1PSIDTS)
	payload.Label = strings.TrimSpace(payload.Label)
	if payload.Secure1PSID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "secure_1psid is required"})
		return
	}
	if payload.Secure1PSIDTS == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "secure_1psidts is required"})
		return
	}
	if payload.Label == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "label is required"})
		return
	}

	sha := sha256.New()
	sha.Write([]byte(payload.Secure1PSID))
	hash := hex.EncodeToString(sha.Sum(nil))
	fileName := fmt.Sprintf("gemini-web-%s.json", hash[:16])

	tokenStorage := &geminiAuth.GeminiWebTokenStorage{
		Secure1PSID:   payload.Secure1PSID,
		Secure1PSIDTS: payload.Secure1PSIDTS,
		Label:         payload.Label,
	}
	// Provide a stable label (gemini-web-<hash>) for logging and identification
	tokenStorage.Label = strings.TrimSuffix(fileName, ".json")

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "gemini-web",
		FileName: fileName,
		Storage:  tokenStorage,
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		log.Errorf("Failed to save Gemini Web token: %v", errSave)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save token"})
		return
	}

	fmt.Printf("Successfully saved Gemini Web token to: %s\n", savedPath)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "file": filepath.Base(savedPath)})
}

func (h *Handler) RequestCodexToken(c *gin.Context) {
	ctx := context.Background()

	fmt.Println("Initializing Codex authentication...")

	// Generate PKCE codes
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		log.Fatalf("Failed to generate PKCE codes: %v", err)
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Fatalf("Failed to generate state parameter: %v", err)
		return
	}

	// Initialize Codex auth service
	openaiAuth := codex.NewCodexAuth(h.cfg)

	// Generate authorization URL
	authURL, err := openaiAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Fatalf("Failed to generate authorization URL: %v", err)
		return
	}

	isWebUI := isWebUIRequest(c)
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/codex/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute codex callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		if _, errStart := startCallbackForwarder(codexCallbackPort, "codex", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start codex callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarder(codexCallbackPort)
		}

		// Wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if time.Now().After(deadline) {
				authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, fmt.Errorf("timeout waiting for OAuth callback"))
				log.Error(codex.GetUserFriendlyMessage(authErr))
				oauthStatus[state] = "Timeout waiting for OAuth callback"
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					oauthErr := codex.NewOAuthError(errStr, "", http.StatusBadRequest)
					log.Error(codex.GetUserFriendlyMessage(oauthErr))
					oauthStatus[state] = "Bad Request"
					return
				}
				if m["state"] != state {
					authErr := codex.NewAuthenticationError(codex.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, m["state"]))
					oauthStatus[state] = "State code error"
					log.Error(codex.GetUserFriendlyMessage(authErr))
					return
				}
				code = m["code"]
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		log.Debug("Authorization code received, exchanging for tokens...")
		// Extract client_id from authURL
		clientID := ""
		if u2, errP := url.Parse(authURL); errP == nil {
			clientID = u2.Query().Get("client_id")
		}
		// Exchange code for tokens with redirect equal to mgmtRedirect
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {clientID},
			"code":          {code},
			"redirect_uri":  {"http://localhost:1455/auth/callback"},
			"code_verifier": {pkceCodes.CodeVerifier},
		}
		httpClient := util.SetProxy(&h.cfg.SDKConfig, &http.Client{})
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://auth.openai.com/oauth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, errDo)
			oauthStatus[state] = "Failed to exchange authorization code for tokens"
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			oauthStatus[state] = fmt.Sprintf("Token exchange failed with status %d", resp.StatusCode)
			log.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(respBody))
			return
		}
		var tokenResp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if errU := json.Unmarshal(respBody, &tokenResp); errU != nil {
			oauthStatus[state] = "Failed to parse token response"
			log.Errorf("failed to parse token response: %v", errU)
			return
		}
		claims, _ := codex.ParseJWTToken(tokenResp.IDToken)
		email := ""
		accountID := ""
		if claims != nil {
			email = claims.GetUserEmail()
			accountID = claims.GetAccountID()
		}
		// Build bundle compatible with existing storage
		bundle := &codex.CodexAuthBundle{
			TokenData: codex.CodexTokenData{
				IDToken:      tokenResp.IDToken,
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				AccountID:    accountID,
				Email:        email,
				Expire:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
			},
			LastRefresh: time.Now().Format(time.RFC3339),
		}

		// Create token storage and persist
		tokenStorage := openaiAuth.CreateTokenStorage(bundle)
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("codex-%s.json", tokenStorage.Email),
			Provider: "codex",
			FileName: fmt.Sprintf("codex-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":      tokenStorage.Email,
				"account_id": tokenStorage.AccountID,
			},
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			oauthStatus[state] = "Failed to save authentication tokens"
			log.Fatalf("Failed to save authentication tokens: %v", errSave)
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Codex services through this CLI")
		delete(oauthStatus, state)
	}()

	oauthStatus[state] = ""
	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestQwenToken(c *gin.Context) {
	ctx := context.Background()

	fmt.Println("Initializing Qwen authentication...")

	state := fmt.Sprintf("gem-%d", time.Now().UnixNano())
	// Initialize Qwen auth service
	qwenAuth := qwen.NewQwenAuth(h.cfg)

	// Generate authorization URL
	deviceFlow, err := qwenAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		log.Fatalf("Failed to generate authorization URL: %v", err)
		return
	}
	authURL := deviceFlow.VerificationURIComplete

	go func() {
		fmt.Println("Waiting for authentication...")
		tokenData, errPollForToken := qwenAuth.PollForToken(deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
		if errPollForToken != nil {
			oauthStatus[state] = "Authentication failed"
			fmt.Printf("Authentication failed: %v\n", errPollForToken)
			return
		}

		// Create token storage
		tokenStorage := qwenAuth.CreateTokenStorage(tokenData)

		tokenStorage.Email = fmt.Sprintf("qwen-%d", time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("qwen-%s.json", tokenStorage.Email),
			Provider: "qwen",
			FileName: fmt.Sprintf("qwen-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: map[string]any{"email": tokenStorage.Email},
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Fatalf("Failed to save authentication tokens: %v", errSave)
			oauthStatus[state] = "Failed to save authentication tokens"
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Qwen services through this CLI")
		delete(oauthStatus, state)
	}()

	oauthStatus[state] = ""
	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestIFlowToken(c *gin.Context) {
	ctx := context.Background()

	fmt.Println("Initializing iFlow authentication...")

	state := fmt.Sprintf("ifl-%d", time.Now().UnixNano())
	authSvc := iflowauth.NewIFlowAuth(h.cfg)
	authURL, redirectURI := authSvc.AuthorizationURL(state, iflowauth.CallbackPort)

	isWebUI := isWebUIRequest(c)
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/iflow/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute iflow callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "callback server unavailable"})
			return
		}
		if _, errStart := startCallbackForwarder(iflowauth.CallbackPort, "iflow", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start iflow callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to start callback server"})
			return
		}

		go func() {
			defer stopCallbackForwarder(iflowauth.CallbackPort)
			fmt.Println("Waiting for authentication...")

			waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-iflow-%s.oauth", state))
			deadline := time.Now().Add(5 * time.Minute)
			var resultMap map[string]string
			for {
				if time.Now().After(deadline) {
					oauthStatus[state] = "Authentication failed"
					fmt.Println("Authentication failed: timeout waiting for callback")
					return
				}
				if data, errR := os.ReadFile(waitFile); errR == nil {
					_ = os.Remove(waitFile)
					_ = json.Unmarshal(data, &resultMap)
					break
				}
				time.Sleep(500 * time.Millisecond)
			}

			if errStr := strings.TrimSpace(resultMap["error"]); errStr != "" {
				oauthStatus[state] = "Authentication failed"
				fmt.Printf("Authentication failed: %s\n", errStr)
				return
			}
			if resultState := strings.TrimSpace(resultMap["state"]); resultState != state {
				oauthStatus[state] = "Authentication failed"
				fmt.Println("Authentication failed: state mismatch")
				return
			}

			code := strings.TrimSpace(resultMap["code"])
			if code == "" {
				oauthStatus[state] = "Authentication failed"
				fmt.Println("Authentication failed: code missing")
				return
			}

			tokenData, errExchange := authSvc.ExchangeCodeForTokens(ctx, code, redirectURI)
			if errExchange != nil {
				oauthStatus[state] = "Authentication failed"
				fmt.Printf("Authentication failed: %v\n", errExchange)
				return
			}

			tokenStorage := authSvc.CreateTokenStorage(tokenData)
			identifier := strings.TrimSpace(tokenStorage.Email)
			if identifier == "" {
				identifier = fmt.Sprintf("iflow-%d", time.Now().UnixMilli())
				tokenStorage.Email = identifier
			}
			record := &coreauth.Auth{
				ID:         fmt.Sprintf("iflow-%s.json", identifier),
				Provider:   "iflow",
				FileName:   fmt.Sprintf("iflow-%s.json", identifier),
				Storage:    tokenStorage,
				Metadata:   map[string]any{"email": identifier, "api_key": tokenStorage.APIKey},
				Attributes: map[string]string{"api_key": tokenStorage.APIKey},
			}

			savedPath, errSave := h.saveTokenRecord(ctx, record)
			if errSave != nil {
				oauthStatus[state] = "Failed to save authentication tokens"
				log.Fatalf("Failed to save authentication tokens: %v", errSave)
				return
			}

			fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
			if tokenStorage.APIKey != "" {
				fmt.Println("API key obtained and saved")
			}
			fmt.Println("You can now use iFlow services through this CLI")
			delete(oauthStatus, state)
		}()

		oauthStatus[state] = ""
		c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
		return
	}

	oauthServer := iflowauth.NewOAuthServer(iflowauth.CallbackPort)
	if err := oauthServer.Start(); err != nil {
		oauthStatus[state] = "Failed to start authentication server"
		log.Errorf("Failed to start iFlow OAuth server: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to start local oauth server"})
		return
	}

	go func() {
		fmt.Println("Waiting for authentication...")
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := oauthServer.Stop(stopCtx); err != nil {
				log.Warnf("Failed to stop iFlow OAuth server: %v", err)
			}
		}()

		result, err := oauthServer.WaitForCallback(5 * time.Minute)
		if err != nil {
			oauthStatus[state] = "Authentication failed"
			fmt.Printf("Authentication failed: %v\n", err)
			return
		}

		if result.Error != "" {
			oauthStatus[state] = "Authentication failed"
			fmt.Printf("Authentication failed: %s\n", result.Error)
			return
		}

		if result.State != state {
			oauthStatus[state] = "Authentication failed"
			fmt.Println("Authentication failed: state mismatch")
			return
		}

		tokenData, errExchange := authSvc.ExchangeCodeForTokens(ctx, result.Code, redirectURI)
		if errExchange != nil {
			oauthStatus[state] = "Authentication failed"
			fmt.Printf("Authentication failed: %v\n", errExchange)
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(tokenData)
		identifier := strings.TrimSpace(tokenStorage.Email)
		if identifier == "" {
			identifier = fmt.Sprintf("iflow-%d", time.Now().UnixMilli())
			tokenStorage.Email = identifier
		}
		record := &coreauth.Auth{
			ID:         fmt.Sprintf("iflow-%s.json", identifier),
			Provider:   "iflow",
			FileName:   fmt.Sprintf("iflow-%s.json", identifier),
			Storage:    tokenStorage,
			Metadata:   map[string]any{"email": identifier, "api_key": tokenStorage.APIKey},
			Attributes: map[string]string{"api_key": tokenStorage.APIKey},
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			oauthStatus[state] = "Failed to save authentication tokens"
			log.Fatalf("Failed to save authentication tokens: %v", errSave)
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if tokenStorage.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use iFlow services through this CLI")
		delete(oauthStatus, state)
	}()

	oauthStatus[state] = ""
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) GetAuthStatus(c *gin.Context) {
	state := c.Query("state")
	if err, ok := oauthStatus[state]; ok {
		if err != "" {
			c.JSON(200, gin.H{"status": "error", "error": err})
		} else {
			c.JSON(200, gin.H{"status": "wait"})
			return
		}
	} else {
		c.JSON(200, gin.H{"status": "ok"})
	}
	delete(oauthStatus, state)
}
