package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
)

// ClaudeTokenStorage extends the existing GeminiTokenStorage for Anthropic-specific data
// It maintains compatibility with the existing auth system while adding Anthropic-specific fields
type ClaudeTokenStorage struct {
	// IDToken is the JWT ID token containing user claims
	IDToken string `json:"id_token"`
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
	// Email is the Anthropic account email
	Email string `json:"email"`
	// Type indicates the type (gemini, chatgpt, claude) of token storage.
	Type string `json:"type"`
	// Expire is the timestamp of the token expire
	Expire string `json:"expired"`
}

// SaveTokenToFile serializes the token storage to a JSON file.
func (ts *ClaudeTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "claude"
	if err := os.MkdirAll(path.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil

}
