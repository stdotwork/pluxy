package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoQwenLogin handles the Qwen device flow using the shared authentication manager.
// It initiates the device-based authentication process for Qwen services and saves
// the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoQwenLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = func(prompt string) (string, error) {
			fmt.Println()
			fmt.Println(prompt)
			var value string
			_, err := fmt.Scanln(&value)
			return value, err
		}
	}

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "qwen", cfg, authOpts)
	if err != nil {
		var emailErr *sdkAuth.EmailRequiredError
		if errors.As(err, &emailErr) {
			log.Error(emailErr.Error())
			return
		}
		log.Fatalf("Qwen authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		log.Infof("Authentication saved to %s", savedPath)
	}

	log.Info("Qwen authentication successful!")
}
