package auth

import (
	"context"
	"errors"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

var ErrRefreshNotSupported = errors.New("cliproxy auth: refresh not supported")

// LoginOptions captures generic knobs shared across authenticators.
// Provider-specific logic can inspect Metadata for extra parameters.
type LoginOptions struct {
	NoBrowser bool
	ProjectID string
	Metadata  map[string]string
	Prompt    func(prompt string) (string, error)
}

// TokenRecord represents credential material produced by an authenticator.
type TokenRecord struct {
	Provider string
	FileName string
	Storage  baseauth.TokenStorage
	Metadata map[string]string
}

// TokenStore persists token records.
type TokenStore interface {
	Save(ctx context.Context, cfg *config.Config, record *TokenRecord) (string, error)
}

// Authenticator manages login and optional refresh flows for a provider.
type Authenticator interface {
	Provider() string
	Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*TokenRecord, error)
	RefreshLead() *time.Duration
}
