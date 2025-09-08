// Package cmd provides command-line interface functionality for the CLI Proxy API.
// It implements the main application commands including login/authentication
// and server startup, handling the complete user onboarding and service lifecycle.
package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/luispater/CLIProxyAPI/internal/auth/claude"
	"github.com/luispater/CLIProxyAPI/internal/browser"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/misc"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
)

// DoClaudeLogin handles the Claude OAuth login process for Anthropic Claude services.
// It initializes the OAuth flow, opens the user's browser for authentication,
// waits for the callback, exchanges the authorization code for tokens,
// and saves the authentication information to a file.
//
// Parameters:
//   - cfg: The application configuration
//   - options: The login options containing browser preferences
func DoClaudeLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

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

	// Initialize OAuth server
	oauthServer := claude.NewOAuthServer(54545)

	// Start OAuth callback server
	if err = oauthServer.Start(); err != nil {
		if strings.Contains(err.Error(), "already in use") {
			authErr := claude.NewAuthenticationError(claude.ErrPortInUse, err)
			log.Error(claude.GetUserFriendlyMessage(authErr))
			os.Exit(13) // Exit code 13 for port-in-use error
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

	// Initialize Claude auth service
	anthropicAuth := claude.NewClaudeAuth(cfg)

	// Generate authorization URL
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
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
			util.PrintSSHTunnelInstructions(54545)
			log.Infof("Please manually open this URL in your browser:\n\n%s\n", authURL)
		} else {
			if err = browser.OpenURL(authURL); err != nil {
				authErr := claude.NewAuthenticationError(claude.ErrBrowserOpenFailed, err)
				log.Warn(claude.GetUserFriendlyMessage(authErr))
				util.PrintSSHTunnelInstructions(54545)
				log.Infof("Please manually open this URL in your browser:\n\n%s\n", authURL)

				// Log platform info for debugging
				platformInfo := browser.GetPlatformInfo()
				log.Debugf("Browser platform info: %+v", platformInfo)
			} else {
				log.Debug("Browser opened successfully")
			}
		}
	} else {
		util.PrintSSHTunnelInstructions(54545)
		log.Infof("Please open this URL in your browser:\n\n%s\n", authURL)
	}

	log.Info("Waiting for authentication callback...")

	// Wait for OAuth callback
	result, err := oauthServer.WaitForCallback(5 * time.Minute)
	if err != nil {
		if strings.Contains(err.Error(), "timeout") {
			authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, err)
			log.Error(claude.GetUserFriendlyMessage(authErr))
		} else {
			log.Errorf("Authentication failed: %v", err)
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
	authBundle, err := anthropicAuth.ExchangeCodeForTokens(ctx, result.Code, state, pkceCodes)
	if err != nil {
		authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, err)
		log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
		log.Debug("This may be due to network issues or invalid authorization code")
		return
	}

	// Create token storage
	tokenStorage := anthropicAuth.CreateTokenStorage(authBundle)

	// Initialize Claude client
	anthropicClient := client.NewClaudeClient(cfg, tokenStorage)

	// Save token storage
	if err = anthropicClient.SaveTokenToFile(); err != nil {
		log.Fatalf("Failed to save authentication tokens: %v", err)
		return
	}

	log.Info("Authentication successful!")
	if authBundle.APIKey != "" {
		log.Info("API key obtained and saved")
	}

	log.Info("You can now use Claude services through this CLI")

}
