// Package gemini provides authentication and token management functionality
// for Google's Gemini AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Gemini API.
package qwen

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
)

// QwenTokenStorage defines the structure for storing OAuth2 token information,
// along with associated user and project details. This data is typically
// serialized to a JSON file for persistence.
type QwenTokenStorage struct {
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
	// ResourceURL is the request base url
	ResourceURL string `json:"resource_url"`
	// Email is the OpenAI account email
	Email string `json:"email"`
	// Type indicates the type (gemini, chatgpt, claude) of token storage.
	Type string `json:"type"`
	// Expire is the timestamp of the token expire
	Expire string `json:"expired"`
}

// SaveTokenToFile serializes the token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path. It ensures the file is
// properly closed after writing.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *QwenTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "qwen"
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
