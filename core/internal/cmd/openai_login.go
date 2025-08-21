// Package cmd provides command-line interface functionality for the CLI Proxy API.
// It implements the main application commands including login/authentication
// and server startup, handling the complete user onboarding and service lifecycle.
package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/luispater/CLIProxyAPI/internal/auth/codex"
	"github.com/luispater/CLIProxyAPI/internal/browser"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
)

// LoginOptions contains options for the Codex login process.
type LoginOptions struct {
	// NoBrowser indicates whether to skip opening the browser automatically.
	NoBrowser bool
}

// DoCodexLogin handles the Codex OAuth login process for OpenAI Codex services.
// It initializes the OAuth flow, opens the user's browser for authentication,
// waits for the callback, exchanges the authorization code for tokens,
// and saves the authentication information to a file.
//
// Parameters:
//   - cfg: The application configuration
//   - options: The login options containing browser preferences
func DoCodexLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	ctx := context.Background()

	log.Info("Initializing Codex authentication...")

	// Generate PKCE codes
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		log.Fatalf("Failed to generate PKCE codes: %v", err)
		return
	}

	// Generate random state parameter
	state, err := generateRandomState()
	if err != nil {
		log.Fatalf("Failed to generate state parameter: %v", err)
		return
	}

	// Initialize OAuth server
	oauthServer := codex.NewOAuthServer(1455)

	// Start OAuth callback server
	if err = oauthServer.Start(); err != nil {
		if strings.Contains(err.Error(), "already in use") {
			authErr := codex.NewAuthenticationError(codex.ErrPortInUse, err)
			log.Error(codex.GetUserFriendlyMessage(authErr))
			os.Exit(13) // Exit code 13 for port-in-use error
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

	// Initialize Codex auth service
	openaiAuth := codex.NewCodexAuth(cfg)

	// Generate authorization URL
	authURL, err := openaiAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Fatalf("Failed to generate authorization URL: %v", err)
		return
	}

	// Open browser or display URL
	if !options.NoBrowser {
		log.Info("Opening browser for authentication...")

		// Check if browser is available
		if !browser.IsAvailable() {
			log.Warn("No browser available on this system")
			log.Infof("Please manually open this URL in your browser:\n\n%s\n", authURL)
		} else {
			if err = browser.OpenURL(authURL); err != nil {
				authErr := codex.NewAuthenticationError(codex.ErrBrowserOpenFailed, err)
				log.Warn(codex.GetUserFriendlyMessage(authErr))
				log.Infof("Please manually open this URL in your browser:\n\n%s\n", authURL)

				// Log platform info for debugging
				platformInfo := browser.GetPlatformInfo()
				log.Debugf("Browser platform info: %+v", platformInfo)
			} else {
				log.Debug("Browser opened successfully")
			}
		}
	} else {
		log.Infof("Please open this URL in your browser:\n\n%s\n", authURL)
	}

	log.Info("Waiting for authentication callback...")

	// Wait for OAuth callback
	result, err := oauthServer.WaitForCallback(5 * time.Minute)
	if err != nil {
		if strings.Contains(err.Error(), "timeout") {
			authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, err)
			log.Error(codex.GetUserFriendlyMessage(authErr))
		} else {
			log.Errorf("Authentication failed: %v", err)
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
	authBundle, err := openaiAuth.ExchangeCodeForTokens(ctx, result.Code, pkceCodes)
	if err != nil {
		authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, err)
		log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
		log.Debug("This may be due to network issues or invalid authorization code")
		return
	}

	// Create token storage
	tokenStorage := openaiAuth.CreateTokenStorage(authBundle)

	// Initialize Codex client
	openaiClient, err := client.NewCodexClient(cfg, tokenStorage)
	if err != nil {
		log.Fatalf("Failed to initialize Codex client: %v", err)
		return
	}

	// Save token storage
	if err = openaiClient.SaveTokenToFile(); err != nil {
		log.Fatalf("Failed to save authentication tokens: %v", err)
		return
	}

	log.Info("Authentication successful!")
	if authBundle.APIKey != "" {
		log.Info("API key obtained and saved")
	}

	log.Info("You can now use Codex services through this CLI")
}

// generateRandomState generates a cryptographically secure random state parameter
// for OAuth2 flows to prevent CSRF attacks.
//
// Returns:
//   - string: A hexadecimal encoded random state string
//   - error: An error if the random generation fails, nil otherwise
func generateRandomState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
