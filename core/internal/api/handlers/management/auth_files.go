package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/auth/claude"
	"github.com/luispater/CLIProxyAPI/internal/auth/codex"
	geminiAuth "github.com/luispater/CLIProxyAPI/internal/auth/gemini"
	"github.com/luispater/CLIProxyAPI/internal/auth/qwen"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/misc"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

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
	if file, err := c.FormFile("file"); err == nil && file != nil {
		name := filepath.Base(file.Filename)
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			c.JSON(400, gin.H{"error": "file must be .json"})
			return
		}
		dst := filepath.Join(h.cfg.AuthDir, name)
		if errSave := c.SaveUploadedFile(file, dst); errSave != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to save file: %v", errSave)})
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
	if errWrite := os.WriteFile(dst, data, 0o600); errWrite != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to write file: %v", errWrite)})
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

// Delete auth files: single by name or all
func (h *Handler) DeleteAuthFile(c *gin.Context) {
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
			if err = os.Remove(full); err == nil {
				deleted++
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
	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "file not found"})
		} else {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to remove file: %v", err)})
		}
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := context.Background()

	log.Info("Initializing Claude authentication...")

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

	// Generate authorization URL
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Fatalf("Failed to generate authorization URL: %v", err)
		return
	}

	go func() {
		// Initialize OAuth server
		oauthServer := claude.NewOAuthServer(54545)

		// Start OAuth callback server
		if err = oauthServer.Start(); err != nil {
			if strings.Contains(err.Error(), "already in use") {
				authErr := claude.NewAuthenticationError(claude.ErrPortInUse, err)
				log.Error(claude.GetUserFriendlyMessage(authErr))
				return
			}
			authErr := claude.NewAuthenticationError(claude.ErrServerStartFailed, err)
			log.Fatalf("Failed to start OAuth callback server: %v", authErr)
			return
		}
		defer func() {
			if err = oauthServer.Stop(ctx); err != nil {
				log.Warnf("Failed to stop OAuth server: %v", err)
			}
		}()

		log.Info("Waiting for authentication callback...")

		// Wait for OAuth callback
		result, errWaitForCallback := oauthServer.WaitForCallback(5 * time.Minute)
		if errWaitForCallback != nil {
			if strings.Contains(errWaitForCallback.Error(), "timeout") {
				authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, errWaitForCallback)
				log.Error(claude.GetUserFriendlyMessage(authErr))
			} else {
				log.Errorf("Authentication failed: %v", errWaitForCallback)
			}
			return
		}

		if result.Error != "" {
			oauthErr := claude.NewOAuthError(result.Error, "", http.StatusBadRequest)
			log.Error(claude.GetUserFriendlyMessage(oauthErr))
			return
		}

		// Validate state parameter
		if result.State != state {
			authErr := claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, result.State))
			log.Error(claude.GetUserFriendlyMessage(authErr))
			return
		}

		log.Debug("Authorization code received, exchanging for tokens...")

		// Exchange authorization code for tokens
		authBundle, errExchangeCodeForTokens := anthropicAuth.ExchangeCodeForTokens(ctx, result.Code, state, pkceCodes)
		if errExchangeCodeForTokens != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, errExchangeCodeForTokens)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			log.Debug("This may be due to network issues or invalid authorization code")
			return
		}

		// Create token storage
		tokenStorage := anthropicAuth.CreateTokenStorage(authBundle)

		// Initialize Claude client
		anthropicClient := client.NewClaudeClient(h.cfg, tokenStorage)

		// Save token storage
		if errWaitForCallback = anthropicClient.SaveTokenToFile(); errWaitForCallback != nil {
			log.Fatalf("Failed to save authentication tokens: %v", errWaitForCallback)
			return
		}

		log.Info("Authentication successful!")
		if authBundle.APIKey != "" {
			log.Info("API key obtained and saved")
		}

		log.Info("You can now use Claude services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL})
}

func (h *Handler) RequestGeminiCLIToken(c *gin.Context) {
	ctx := context.Background()

	// Optional project ID from query
	projectID := c.Query("project_id")

	log.Info("Initializing Google authentication...")

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
	authURL := conf.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	go func() {
		codeChan := make(chan string)
		errChan := make(chan error)

		mux := http.NewServeMux()
		server := &http.Server{Addr: ":8085", Handler: mux}

		mux.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
			if err := r.URL.Query().Get("error"); err != "" {
				_, _ = fmt.Fprintf(w, "Authentication failed: %s", err)
				errChan <- fmt.Errorf("authentication failed via callback: %s", err)
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				_, _ = fmt.Fprint(w, "Authentication failed: code not found.")
				errChan <- fmt.Errorf("code not found in callback")
				return
			}
			_, _ = fmt.Fprint(w, "<html><body><h1>Authentication successful!</h1><p>You can close this window.</p></body></html>")
			codeChan <- code
		})

		go func() {
			if errListen := server.ListenAndServe(); errListen != nil && errListen != http.ErrServerClosed {
				log.Fatalf("ListenAndServe(): %v", errListen)
			}
		}()

		log.Info("Waiting for authentication callback...")

		var authCode string
		select {
		case code := <-codeChan:
			authCode = code
		case errCallback := <-errChan:
			log.Errorf("Authentication failed: %v", errCallback)
			// Attempt graceful shutdown
			if errShutdown := server.Shutdown(ctx); errShutdown != nil {
				log.Warnf("Failed to shut down server: %v", errShutdown)
			}
			return
		case <-time.After(5 * time.Minute):
			log.Error("oauth flow timed out")
			if errShutdown := server.Shutdown(ctx); errShutdown != nil {
				log.Warnf("Failed to shut down server after timeout: %v", errShutdown)
			}
			return
		}

		// Shutdown the callback server after receiving the code
		if errShutdown := server.Shutdown(ctx); errShutdown != nil {
			log.Warnf("Failed to shut down server: %v", errShutdown)
		}

		// Exchange authorization code for token
		token, err := conf.Exchange(ctx, authCode)
		if err != nil {
			log.Errorf("Failed to exchange token: %v", err)
			return
		}

		// Create token storage (mirrors internal/auth/gemini createTokenStorage)
		httpClient := conf.Client(ctx, token)
		req, errNewRequest := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
		if errNewRequest != nil {
			log.Errorf("Could not get user info: %v", errNewRequest)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			log.Errorf("Failed to execute request: %v", errDo)
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
			return
		}

		email := gjson.GetBytes(bodyBytes, "email").String()
		if email != "" {
			log.Infof("Authenticated user email: %s", email)
		} else {
			log.Info("Failed to get user email from token")
		}

		// Marshal/unmarshal oauth2.Token to generic map and enrich fields
		var ifToken map[string]any
		jsonData, _ := json.Marshal(token)
		if errUnmarshal := json.Unmarshal(jsonData, &ifToken); errUnmarshal != nil {
			log.Errorf("Failed to unmarshal token: %v", errUnmarshal)
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
		httpClient2, errGetClient := gemAuth.GetAuthenticatedClient(ctx, &ts, h.cfg, true)
		if errGetClient != nil {
			log.Fatalf("failed to get authenticated client: %v", errGetClient)
			return
		}
		log.Info("Authentication successful.")

		// Initialize the API client
		cliClient := client.NewGeminiCLIClient(httpClient2, &ts, h.cfg)

		// Perform the user setup process (migrated from DoLogin)
		if err = cliClient.SetupUser(ctx, ts.Email, projectID); err != nil {
			if err.Error() == "failed to start user onboarding, need define a project id" {
				log.Error("Failed to start user onboarding: A project ID is required.")
				project, errGetProjectList := cliClient.GetProjectList(ctx)
				if errGetProjectList != nil {
					log.Fatalf("Failed to get project list: %v", err)
				} else {
					log.Infof("Your account %s needs to specify a project ID.", ts.Email)
					log.Info("========================================================================")
					for _, p := range project.Projects {
						log.Infof("Project ID: %s", p.ProjectID)
						log.Infof("Project Name: %s", p.Name)
						log.Info("------------------------------------------------------------------------")
					}
					log.Infof("Please run this command to login again with a specific project:\n\n%s --login --project_id <project_id>\n", os.Args[0])
				}
			} else {
				log.Fatalf("Failed to complete user setup: %v", err)
			}
			return
		}

		// Post-setup checks and token persistence
		auto := projectID == ""
		cliClient.SetIsAuto(auto)
		if !cliClient.IsChecked() && !cliClient.IsAuto() {
			isChecked, checkErr := cliClient.CheckCloudAPIIsEnabled()
			if checkErr != nil {
				log.Fatalf("Failed to check if Cloud AI API is enabled: %v", checkErr)
				return
			}
			cliClient.SetIsChecked(isChecked)
			if !isChecked {
				log.Fatal("Failed to check if Cloud AI API is enabled. If you encounter an error message, please create an issue.")
				return
			}
		}

		if err = cliClient.SaveTokenToFile(); err != nil {
			log.Fatalf("Failed to save token to file: %v", err)
			return
		}

		log.Info("You can now use Gemini CLI services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL})
}

func (h *Handler) RequestCodexToken(c *gin.Context) {
	ctx := context.Background()

	log.Info("Initializing Codex authentication...")

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

	go func() {
		// Initialize OAuth server
		oauthServer := codex.NewOAuthServer(1455)

		// Start OAuth callback server
		if err = oauthServer.Start(); err != nil {
			if strings.Contains(err.Error(), "already in use") {
				authErr := codex.NewAuthenticationError(codex.ErrPortInUse, err)
				log.Error(codex.GetUserFriendlyMessage(authErr))
				return
			}
			authErr := codex.NewAuthenticationError(codex.ErrServerStartFailed, err)
			log.Fatalf("Failed to start OAuth callback server: %v", authErr)
			return
		}
		defer func() {
			if err = oauthServer.Stop(ctx); err != nil {
				log.Warnf("Failed to stop OAuth server: %v", err)
			}
		}()

		log.Info("Waiting for authentication callback...")

		// Wait for OAuth callback
		result, errWaitForCallback := oauthServer.WaitForCallback(5 * time.Minute)
		if errWaitForCallback != nil {
			if strings.Contains(errWaitForCallback.Error(), "timeout") {
				authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, errWaitForCallback)
				log.Error(codex.GetUserFriendlyMessage(authErr))
			} else {
				log.Errorf("Authentication failed: %v", errWaitForCallback)
			}
			return
		}

		if result.Error != "" {
			oauthErr := codex.NewOAuthError(result.Error, "", http.StatusBadRequest)
			log.Error(codex.GetUserFriendlyMessage(oauthErr))
			return
		}

		// Validate state parameter
		if result.State != state {
			authErr := codex.NewAuthenticationError(codex.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, result.State))
			log.Error(codex.GetUserFriendlyMessage(authErr))
			return
		}

		log.Debug("Authorization code received, exchanging for tokens...")

		// Exchange authorization code for tokens
		authBundle, errExchangeCodeForTokens := openaiAuth.ExchangeCodeForTokens(ctx, result.Code, pkceCodes)
		if errExchangeCodeForTokens != nil {
			authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, errExchangeCodeForTokens)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			log.Debug("This may be due to network issues or invalid authorization code")
			return
		}

		// Create token storage
		tokenStorage := openaiAuth.CreateTokenStorage(authBundle)

		// Initialize Codex client
		openaiClient, errWaitForCallback := client.NewCodexClient(h.cfg, tokenStorage)
		if errWaitForCallback != nil {
			log.Fatalf("Failed to initialize Codex client: %v", errWaitForCallback)
			return
		}

		// Save token storage
		if errWaitForCallback = openaiClient.SaveTokenToFile(); errWaitForCallback != nil {
			log.Fatalf("Failed to save authentication tokens: %v", errWaitForCallback)
			return
		}

		log.Info("Authentication successful!")
		if authBundle.APIKey != "" {
			log.Info("API key obtained and saved")
		}

		log.Info("You can now use Codex services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL})
}

func (h *Handler) RequestQwenToken(c *gin.Context) {
	ctx := context.Background()

	log.Info("Initializing Qwen authentication...")

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
		log.Info("Waiting for authentication...")
		tokenData, errPollForToken := qwenAuth.PollForToken(deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
		if errPollForToken != nil {
			fmt.Printf("Authentication failed: %v\n", errPollForToken)
			os.Exit(1)
		}

		// Create token storage
		tokenStorage := qwenAuth.CreateTokenStorage(tokenData)

		// Initialize Qwen client
		qwenClient := client.NewQwenClient(h.cfg, tokenStorage)

		tokenStorage.Email = fmt.Sprintf("qwen-%d", time.Now().UnixMilli())

		// Save token storage
		if err = qwenClient.SaveTokenToFile(); err != nil {
			log.Fatalf("Failed to save authentication tokens: %v", err)
			return
		}

		log.Info("Authentication successful!")
		log.Info("You can now use Qwen services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL})
}
