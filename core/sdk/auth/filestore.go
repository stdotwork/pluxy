package auth

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// FileTokenStore persists token records into the configured auth directory using the
// filename suggested by the authenticator. Relative paths are resolved against cfg.AuthDir.
type FileTokenStore struct{}

// NewFileTokenStore creates a token store that saves credentials to disk through the
// TokenStorage implementation embedded in the token record.
func NewFileTokenStore() *FileTokenStore {
	return &FileTokenStore{}
}

// Save writes the token storage to the resolved file path.
func (s *FileTokenStore) Save(ctx context.Context, cfg *config.Config, record *TokenRecord) (string, error) {
	if record == nil || record.Storage == nil {
		return "", fmt.Errorf("cliproxy auth: token record is incomplete")
	}
	target := record.FileName
	if target == "" {
		return "", fmt.Errorf("cliproxy auth: missing file name for provider %s", record.Provider)
	}
	if cfg != nil && !filepath.IsAbs(target) {
		target = filepath.Join(cfg.AuthDir, target)
	}
	if err := record.Storage.SaveTokenToFile(target); err != nil {
		return "", err
	}
	return target, nil
}
