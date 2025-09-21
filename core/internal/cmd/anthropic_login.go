package cmd

import (
	"context"
	"errors"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoClaudeLogin triggers the Claude OAuth flow through the shared authentication manager.
func DoClaudeLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	}

	_, savedPath, err := manager.Login(context.Background(), "claude", cfg, authOpts)
	if err != nil {
		var authErr *claude.AuthenticationError
		if errors.As(err, &authErr) {
			log.Error(claude.GetUserFriendlyMessage(authErr))
			if authErr.Type == claude.ErrPortInUse.Type {
				os.Exit(claude.ErrPortInUse.Code)
			}
			return
		}
		log.Fatalf("Claude authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		log.Infof("Authentication saved to %s", savedPath)
	}

	log.Info("Claude authentication successful!")
}
