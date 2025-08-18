package claude

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// GeneratePKCECodes generates a PKCE code verifier and challenge pair
// following RFC 7636 specifications for OAuth 2.0 PKCE extension
func GeneratePKCECodes() (*PKCECodes, error) {
	// Generate code verifier: 43-128 characters, URL-safe
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}

	// Generate code challenge using S256 method
	codeChallenge := generateCodeChallenge(codeVerifier)

	return &PKCECodes{
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
	}, nil
}

// generateCodeVerifier creates a cryptographically random string
// of 128 characters using URL-safe base64 encoding
func generateCodeVerifier() (string, error) {
	// Generate 96 random bytes (will result in 128 base64 characters)
	bytes := make([]byte, 96)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode to URL-safe base64 without padding
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(bytes), nil
}

// generateCodeChallenge creates a SHA256 hash of the code verifier
// and encodes it using URL-safe base64 encoding without padding
func generateCodeChallenge(codeVerifier string) string {
	hash := sha256.Sum256([]byte(codeVerifier))
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hash[:])
}
