package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/auth/codex"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	chatGPTEndpoint = "https://chatgpt.com/backend-api"
)

// CodexClient implements the Client interface for OpenAI API
type CodexClient struct {
	ClientBase
	codexAuth *codex.CodexAuth
}

// NewCodexClient creates a new OpenAI client instance
func NewCodexClient(cfg *config.Config, ts *codex.CodexTokenStorage) (*CodexClient, error) {
	httpClient := util.SetProxy(cfg, &http.Client{})
	client := &CodexClient{
		ClientBase: ClientBase{
			RequestMutex:       &sync.Mutex{},
			httpClient:         httpClient,
			cfg:                cfg,
			modelQuotaExceeded: make(map[string]*time.Time),
			tokenStorage:       ts,
		},
		codexAuth: codex.NewCodexAuth(cfg),
	}

	return client, nil
}

// GetUserAgent returns the user agent string for OpenAI API requests
func (c *CodexClient) GetUserAgent() string {
	return "codex-cli"
}

func (c *CodexClient) TokenStorage() auth.TokenStorage {
	return c.tokenStorage
}

// SendMessage sends a message to OpenAI API (non-streaming)
func (c *CodexClient) SendMessage(_ context.Context, _ []byte, _ string, _ *Content, _ []Content, _ []ToolDeclaration) ([]byte, *ErrorMessage) {
	// For now, return an error as OpenAI integration is not fully implemented
	return nil, &ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("codex message sending not yet implemented"),
	}
}

// SendMessageStream sends a streaming message to OpenAI API
func (c *CodexClient) SendMessageStream(_ context.Context, _ []byte, _ string, _ *Content, _ []Content, _ []ToolDeclaration, _ ...bool) (<-chan []byte, <-chan *ErrorMessage) {
	errChan := make(chan *ErrorMessage, 1)
	errChan <- &ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("codex streaming not yet implemented"),
	}
	close(errChan)

	return nil, errChan
}

// SendRawMessage sends a raw message to OpenAI API
func (c *CodexClient) SendRawMessage(ctx context.Context, rawJSON []byte, alt string) ([]byte, *ErrorMessage) {
	modelResult := gjson.GetBytes(rawJSON, "model")
	model := modelResult.String()
	modelName := model

	respBody, err := c.APIRequest(ctx, "/codex/responses", rawJSON, alt, false)
	if err != nil {
		if err.StatusCode == 429 {
			now := time.Now()
			c.modelQuotaExceeded[modelName] = &now
		}
		return nil, err
	}
	delete(c.modelQuotaExceeded, modelName)
	bodyBytes, errReadAll := io.ReadAll(respBody)
	if errReadAll != nil {
		return nil, &ErrorMessage{StatusCode: 500, Error: errReadAll}
	}
	return bodyBytes, nil

}

// SendRawMessageStream sends a raw streaming message to OpenAI API
func (c *CodexClient) SendRawMessageStream(ctx context.Context, rawJSON []byte, alt string) (<-chan []byte, <-chan *ErrorMessage) {
	errChan := make(chan *ErrorMessage)
	dataChan := make(chan []byte)
	go func() {
		defer close(errChan)
		defer close(dataChan)

		modelResult := gjson.GetBytes(rawJSON, "model")
		model := modelResult.String()
		modelName := model
		var stream io.ReadCloser
		for {
			var err *ErrorMessage
			stream, err = c.APIRequest(ctx, "/codex/responses", rawJSON, alt, true)
			if err != nil {
				if err.StatusCode == 429 {
					now := time.Now()
					c.modelQuotaExceeded[modelName] = &now
				}
				errChan <- err
				return
			}
			delete(c.modelQuotaExceeded, modelName)
			break
		}

		scanner := bufio.NewScanner(stream)
		buffer := make([]byte, 10240*1024)
		scanner.Buffer(buffer, 10240*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			dataChan <- line
		}

		if errScanner := scanner.Err(); errScanner != nil {
			errChan <- &ErrorMessage{500, errScanner}
			_ = stream.Close()
			return
		}

		_ = stream.Close()
	}()

	return dataChan, errChan
}

// SendRawTokenCount sends a token count request to OpenAI API
func (c *CodexClient) SendRawTokenCount(_ context.Context, _ []byte, _ string) ([]byte, *ErrorMessage) {
	return nil, &ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("codex token counting not yet implemented"),
	}
}

// SaveTokenToFile persists the token storage to disk
func (c *CodexClient) SaveTokenToFile() error {
	fileName := filepath.Join(c.cfg.AuthDir, fmt.Sprintf("codex-%s.json", c.tokenStorage.(*codex.CodexTokenStorage).Email))
	return c.tokenStorage.SaveTokenToFile(fileName)
}

// RefreshTokens refreshes the access tokens if needed
func (c *CodexClient) RefreshTokens(ctx context.Context) error {
	if c.tokenStorage == nil || c.tokenStorage.(*codex.CodexTokenStorage).RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	// Refresh tokens using the auth service
	newTokenData, err := c.codexAuth.RefreshTokensWithRetry(ctx, c.tokenStorage.(*codex.CodexTokenStorage).RefreshToken, 3)
	if err != nil {
		return fmt.Errorf("failed to refresh tokens: %w", err)
	}

	// Update token storage
	c.codexAuth.UpdateTokenStorage(c.tokenStorage.(*codex.CodexTokenStorage), newTokenData)

	// Save updated tokens
	if err = c.SaveTokenToFile(); err != nil {
		log.Warnf("Failed to save refreshed tokens: %v", err)
	}

	log.Debug("codex tokens refreshed successfully")
	return nil
}

// APIRequest handles making requests to the CLI API endpoints.
func (c *CodexClient) APIRequest(ctx context.Context, endpoint string, body interface{}, _ string, _ bool) (io.ReadCloser, *ErrorMessage) {
	var jsonBody []byte
	var err error
	if byteBody, ok := body.([]byte); ok {
		jsonBody = byteBody
	} else {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, &ErrorMessage{500, fmt.Errorf("failed to marshal request body: %w", err)}
		}
	}

	url := fmt.Sprintf("%s/%s", chatGPTEndpoint, endpoint)

	// log.Debug(string(jsonBody))
	// log.Debug(url)
	reqBody := bytes.NewBuffer(jsonBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to create request: %v", err)}
	}

	sessionID := uuid.New().String()
	// Set headers
	req.Header.Set("Version", "0.21.0")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Openai-Beta", "responses=experimental")
	req.Header.Set("Session_id", sessionID)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Chatgpt-Account-Id", c.tokenStorage.(*codex.CodexTokenStorage).AccountID)
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.tokenStorage.(*codex.CodexTokenStorage).AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to execute request: %v", err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			if err = resp.Body.Close(); err != nil {
				log.Printf("warn: failed to close response body: %v", err)
			}
		}()
		bodyBytes, _ := io.ReadAll(resp.Body)
		// log.Debug(string(jsonBody))
		return nil, &ErrorMessage{resp.StatusCode, fmt.Errorf(string(bodyBytes))}
	}

	return resp.Body, nil
}

func (c *CodexClient) GetEmail() string {
	return c.tokenStorage.(*codex.CodexTokenStorage).Email
}

// IsModelQuotaExceeded returns true if the specified model has exceeded its quota
// and no fallback options are available.
func (c *CodexClient) IsModelQuotaExceeded(model string) bool {
	if lastExceededTime, hasKey := c.modelQuotaExceeded[model]; hasKey {
		duration := time.Now().Sub(*lastExceededTime)
		if duration > 30*time.Minute {
			return false
		}
		return true
	}
	return false
}
