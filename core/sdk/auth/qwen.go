package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qwen"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	// legacy client removed
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// QwenAuthenticator implements the device flow login for Qwen accounts.
type QwenAuthenticator struct{}

// NewQwenAuthenticator constructs a Qwen authenticator.
func NewQwenAuthenticator() *QwenAuthenticator {
	return &QwenAuthenticator{}
}

func (a *QwenAuthenticator) Provider() string {
	return "qwen"
}

func (a *QwenAuthenticator) RefreshLead() *time.Duration {
	d := 3 * time.Hour
	return &d
}

func (a *QwenAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*TokenRecord, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := qwen.NewQwenAuth(cfg)

	deviceFlow, err := authSvc.InitiateDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("qwen device flow initiation failed: %w", err)
	}

	authURL := deviceFlow.VerificationURIComplete

	if !opts.NoBrowser {
		log.Info("Opening browser for Qwen authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			log.Infof("Visit the following URL to continue authentication:\n%s", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			log.Infof("Visit the following URL to continue authentication:\n%s", authURL)
		}
	} else {
		log.Infof("Visit the following URL to continue authentication:\n%s", authURL)
	}

	log.Info("Waiting for Qwen authentication...")

	tokenData, err := authSvc.PollForToken(deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
	if err != nil {
		return nil, fmt.Errorf("qwen authentication failed: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(tokenData)

	email := ""
	if opts.Metadata != nil {
		email = opts.Metadata["email"]
		if email == "" {
			email = opts.Metadata["alias"]
		}
	}

	if email == "" && opts.Prompt != nil {
		email, err = opts.Prompt("Please input your email address or alias for Qwen:")
		if err != nil {
			return nil, err
		}
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil, &EmailRequiredError{Prompt: "Please provide an email address or alias for Qwen."}
	}

	tokenStorage.Email = email

	// no legacy client construction

	fileName := fmt.Sprintf("qwen-%s.json", tokenStorage.Email)
	metadata := map[string]string{
		"email": tokenStorage.Email,
	}

	log.Info("Qwen authentication successful")

	return &TokenRecord{
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}

func (a *QwenAuthenticator) Refresh(ctx context.Context, cfg *config.Config, record *TokenRecord) (*TokenRecord, error) {
	if record == nil || record.Storage == nil {
		return nil, fmt.Errorf("cliproxy auth: empty token record for qwen refresh")
	}
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	storage, ok := record.Storage.(*qwen.QwenTokenStorage)
	if !ok {
		return nil, fmt.Errorf("cliproxy auth: unexpected token storage type for qwen refresh")
	}

	svc := qwen.NewQwenAuth(cfg)
	td, err := svc.RefreshTokens(ctx, storage.RefreshToken)
	if err != nil {
		return nil, err
	}
	storage.AccessToken = td.AccessToken
	storage.RefreshToken = td.RefreshToken
	storage.ResourceURL = td.ResourceURL
	storage.Expire = td.Expire

	result := &TokenRecord{
		Provider: a.Provider(),
		FileName: record.FileName,
		Storage:  storage,
		Metadata: record.Metadata,
	}
	return result, nil
}
