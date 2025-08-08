package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
)

// CodexTokenStorage extends the existing GeminiTokenStorage for OpenAI-specific data
// It maintains compatibility with the existing auth system while adding OpenAI-specific fields
type CodexTokenStorage struct {
	// IDToken is the JWT ID token containing user claims
	IDToken string `json:"id_token"`
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// AccountID is the OpenAI account identifier
	AccountID string `json:"account_id"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
	// Email is the OpenAI account email
	Email string `json:"email"`
	// Type indicates the type (gemini, chatgpt, claude) of token storage.
	Type string `json:"type"`
	// Expire is the timestamp of the token expire
	Expire string `json:"expired"`
}

// SaveTokenToFile serializes the token storage to a JSON file.
func (ts *CodexTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "codex"
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
