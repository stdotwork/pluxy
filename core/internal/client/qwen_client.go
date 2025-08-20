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
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/auth/qwen"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	qwenEndpoint = "https://portal.qwen.ai/v1"
)

// QwenClient implements the Client interface for OpenAI API
type QwenClient struct {
	ClientBase
	qwenAuth *qwen.QwenAuth
}

// NewQwenClient creates a new OpenAI client instance
func NewQwenClient(cfg *config.Config, ts *qwen.QwenTokenStorage) *QwenClient {
	httpClient := util.SetProxy(cfg, &http.Client{})
	client := &QwenClient{
		ClientBase: ClientBase{
			RequestMutex:       &sync.Mutex{},
			httpClient:         httpClient,
			cfg:                cfg,
			modelQuotaExceeded: make(map[string]*time.Time),
			tokenStorage:       ts,
		},
		qwenAuth: qwen.NewQwenAuth(cfg),
	}

	return client
}

// GetUserAgent returns the user agent string for OpenAI API requests
func (c *QwenClient) GetUserAgent() string {
	return "google-api-nodejs-client/9.15.1"
}

func (c *QwenClient) TokenStorage() auth.TokenStorage {
	return c.tokenStorage
}

// SendMessage sends a message to OpenAI API (non-streaming)
func (c *QwenClient) SendMessage(_ context.Context, _ []byte, _ string, _ *Content, _ []Content, _ []ToolDeclaration) ([]byte, *ErrorMessage) {
	// For now, return an error as OpenAI integration is not fully implemented
	return nil, &ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("qwen message sending not yet implemented"),
	}
}

// SendMessageStream sends a streaming message to OpenAI API
func (c *QwenClient) SendMessageStream(_ context.Context, _ []byte, _ string, _ *Content, _ []Content, _ []ToolDeclaration, _ ...bool) (<-chan []byte, <-chan *ErrorMessage) {
	errChan := make(chan *ErrorMessage, 1)
	errChan <- &ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("qwen streaming not yet implemented"),
	}
	close(errChan)

	return nil, errChan
}

// SendRawMessage sends a raw message to OpenAI API
func (c *QwenClient) SendRawMessage(ctx context.Context, rawJSON []byte, alt string) ([]byte, *ErrorMessage) {
	modelResult := gjson.GetBytes(rawJSON, "model")
	model := modelResult.String()
	modelName := model

	respBody, err := c.APIRequest(ctx, "/chat/completions", rawJSON, alt, false)
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
func (c *QwenClient) SendRawMessageStream(ctx context.Context, rawJSON []byte, alt string) (<-chan []byte, <-chan *ErrorMessage) {
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
			stream, err = c.APIRequest(ctx, "/chat/completions", rawJSON, alt, true)
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
			errChan <- &ErrorMessage{500, errScanner, nil}
			_ = stream.Close()
			return
		}

		_ = stream.Close()
	}()

	return dataChan, errChan
}

// SendRawTokenCount sends a token count request to OpenAI API
func (c *QwenClient) SendRawTokenCount(_ context.Context, _ []byte, _ string) ([]byte, *ErrorMessage) {
	return nil, &ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("qwen token counting not yet implemented"),
	}
}

// SaveTokenToFile persists the token storage to disk
func (c *QwenClient) SaveTokenToFile() error {
	fileName := filepath.Join(c.cfg.AuthDir, fmt.Sprintf("qwen-%s.json", c.tokenStorage.(*qwen.QwenTokenStorage).Email))
	return c.tokenStorage.SaveTokenToFile(fileName)
}

// RefreshTokens refreshes the access tokens if needed
func (c *QwenClient) RefreshTokens(ctx context.Context) error {
	if c.tokenStorage == nil || c.tokenStorage.(*qwen.QwenTokenStorage).RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	// Refresh tokens using the auth service
	newTokenData, err := c.qwenAuth.RefreshTokensWithRetry(ctx, c.tokenStorage.(*qwen.QwenTokenStorage).RefreshToken, 3)
	if err != nil {
		return fmt.Errorf("failed to refresh tokens: %w", err)
	}

	// Update token storage
	c.qwenAuth.UpdateTokenStorage(c.tokenStorage.(*qwen.QwenTokenStorage), newTokenData)

	// Save updated tokens
	if err = c.SaveTokenToFile(); err != nil {
		log.Warnf("Failed to save refreshed tokens: %v", err)
	}

	log.Debug("qwen tokens refreshed successfully")
	return nil
}

// APIRequest handles making requests to the CLI API endpoints.
func (c *QwenClient) APIRequest(ctx context.Context, endpoint string, body interface{}, _ string, _ bool) (io.ReadCloser, *ErrorMessage) {
	var jsonBody []byte
	var err error
	if byteBody, ok := body.([]byte); ok {
		jsonBody = byteBody
	} else {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, &ErrorMessage{500, fmt.Errorf("failed to marshal request body: %w", err), nil}
		}
	}

	streamResult := gjson.GetBytes(jsonBody, "stream")
	if streamResult.Exists() && streamResult.Type == gjson.True {
		jsonBody, _ = sjson.SetBytes(jsonBody, "stream_options.include_usage", true)
	}

	var url string
	if c.tokenStorage.(*qwen.QwenTokenStorage).ResourceURL == "" {
		url = fmt.Sprintf("https://%s/v1%s", c.tokenStorage.(*qwen.QwenTokenStorage).ResourceURL, endpoint)
	} else {
		url = fmt.Sprintf("%s%s", qwenEndpoint, endpoint)
	}

	// log.Debug(string(jsonBody))
	// log.Debug(url)
	reqBody := bytes.NewBuffer(jsonBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to create request: %v", err), nil}
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.GetUserAgent())
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
	req.Header.Set("Client-Metadata", c.getClientMetadataString())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.tokenStorage.(*qwen.QwenTokenStorage).AccessToken))

	if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
		ginContext.Set("API_REQUEST", jsonBody)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to execute request: %v", err), nil}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			if err = resp.Body.Close(); err != nil {
				log.Printf("warn: failed to close response body: %v", err)
			}
		}()
		bodyBytes, _ := io.ReadAll(resp.Body)
		// log.Debug(string(jsonBody))
		return nil, &ErrorMessage{resp.StatusCode, fmt.Errorf(string(bodyBytes)), nil}
	}

	return resp.Body, nil
}

func (c *QwenClient) getClientMetadata() map[string]string {
	return map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
		// "pluginVersion": pluginVersion,
	}
}

func (c *QwenClient) getClientMetadataString() string {
	md := c.getClientMetadata()
	parts := make([]string, 0, len(md))
	for k, v := range md {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

func (c *QwenClient) GetEmail() string {
	return c.tokenStorage.(*qwen.QwenTokenStorage).Email
}

// IsModelQuotaExceeded returns true if the specified model has exceeded its quota
// and no fallback options are available.
func (c *QwenClient) IsModelQuotaExceeded(model string) bool {
	if lastExceededTime, hasKey := c.modelQuotaExceeded[model]; hasKey {
		duration := time.Now().Sub(*lastExceededTime)
		if duration > 30*time.Minute {
			return false
		}
		return true
	}
	return false
}
