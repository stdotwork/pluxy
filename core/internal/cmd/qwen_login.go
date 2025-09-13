// Package cmd provides command-line interface functionality for the CLI Proxy API.
// It implements the main application commands including login/authentication
// and server startup, handling the complete user onboarding and service lifecycle.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/luispater/CLIProxyAPI/v5/internal/auth/qwen"
	"github.com/luispater/CLIProxyAPI/v5/internal/browser"
	"github.com/luispater/CLIProxyAPI/v5/internal/client"
	"github.com/luispater/CLIProxyAPI/v5/internal/config"
	log "github.com/sirupsen/logrus"
)

// DoQwenLogin handles the Qwen OAuth login process for Alibaba Qwen services.
// It initializes the OAuth flow, opens the user's browser for authentication,
// waits for the callback, exchanges the authorization code for tokens,
// and saves the authentication information to a file.
//
// Parameters:
//   - cfg: The application configuration
//   - options: The login options containing browser preferences
func DoQwenLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	ctx := context.Background()

	log.Info("Initializing Qwen authentication...")

	// Initialize Qwen auth service
	qwenAuth := qwen.NewQwenAuth(cfg)

	// Generate authorization URL
	deviceFlow, err := qwenAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		log.Fatalf("Failed to generate authorization URL: %v", err)
		return
	}
	authURL := deviceFlow.VerificationURIComplete

	// Open browser or display URL
	if !options.NoBrowser {
		log.Info("Opening browser for authentication...")

		// Check if browser is available
		if !browser.IsAvailable() {
			log.Warn("No browser available on this system")
			log.Infof("Please manually open this URL in your browser:\n\n%s\n", authURL)
		} else {
			if err = browser.OpenURL(authURL); err != nil {
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

	log.Info("Waiting for authentication...")
	tokenData, err := qwenAuth.PollForToken(deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
	if err != nil {
		fmt.Printf("Authentication failed: %v\n", err)
		os.Exit(1)
	}

	// Create token storage
	tokenStorage := qwenAuth.CreateTokenStorage(tokenData)

	// Initialize Qwen client
	qwenClient := client.NewQwenClient(cfg, tokenStorage)

	fmt.Println("\nPlease input your email address or any alias:")
	var email string
	_, _ = fmt.Scanln(&email)
	tokenStorage.Email = email

	// Save token storage
	if err = qwenClient.SaveTokenToFile(); err != nil {
		log.Fatalf("Failed to save authentication tokens: %v", err)
		return
	}

	log.Info("Authentication successful!")
	log.Info("You can now use Qwen services through this CLI")
}
