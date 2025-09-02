// Package client defines the interface and base structure for AI API clients.
// It provides a common interface that all supported AI service clients must implement,
// including methods for sending messages, handling streams, and managing authentication.
package client

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/config"
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/registry"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// OpenAICompatibilityClient implements the Client interface for external OpenAI-compatible API providers.
// This client handles requests to external services that support OpenAI-compatible APIs,
// such as OpenRouter, Together.ai, and other similar services.
type OpenAICompatibilityClient struct {
	ClientBase
	compatConfig       *config.OpenAICompatibility
	currentAPIKeyIndex int
}

// NewOpenAICompatibilityClient creates a new OpenAI compatibility client instance.
//
// Parameters:
//   - cfg: The application configuration.
//   - compatConfig: The OpenAI compatibility configuration for the specific provider.
//
// Returns:
//   - *OpenAICompatibilityClient: A new OpenAI compatibility client instance.
//   - error: An error if the client creation fails.
func NewOpenAICompatibilityClient(cfg *config.Config, compatConfig *config.OpenAICompatibility) (*OpenAICompatibilityClient, error) {
	if compatConfig == nil {
		return nil, fmt.Errorf("compatibility configuration is required")
	}

	if len(compatConfig.APIKeys) == 0 {
		return nil, fmt.Errorf("at least one API key is required for OpenAI compatibility provider: %s", compatConfig.Name)
	}

	httpClient := util.SetProxy(cfg, &http.Client{})

	// Generate unique client ID
	clientID := fmt.Sprintf("openai-compatibility-%s-%d", compatConfig.Name, time.Now().UnixNano())

	client := &OpenAICompatibilityClient{
		ClientBase: ClientBase{
			RequestMutex:       &sync.Mutex{},
			httpClient:         httpClient,
			cfg:                cfg,
			modelQuotaExceeded: make(map[string]*time.Time),
		},
		compatConfig:       compatConfig,
		currentAPIKeyIndex: 0,
	}

	// Initialize model registry
	client.InitializeModelRegistry(clientID)

	// Convert compatibility models to registry models and register them
	registryModels := make([]*registry.ModelInfo, 0, len(compatConfig.Models))
	for _, model := range compatConfig.Models {
		registryModel := &registry.ModelInfo{
			ID:          model.Alias,
			Object:      "model",
			Created:     time.Now().Unix(),
			OwnedBy:     compatConfig.Name,
			Type:        "openai-compatibility",
			DisplayName: model.Name,
		}
		registryModels = append(registryModels, registryModel)
	}

	client.RegisterModels(compatConfig.Name, registryModels)

	return client, nil
}

// Type returns the client type.
func (c *OpenAICompatibilityClient) Type() string {
	return OPENAI
}

// Provider returns the provider name for this client.
func (c *OpenAICompatibilityClient) Provider() string {
	return c.compatConfig.Name
}

// CanProvideModel checks if this client can provide the specified model alias.
//
// Parameters:
//   - modelName: The name/alias of the model to check.
//
// Returns:
//   - bool: True if the model alias is supported, false otherwise.
func (c *OpenAICompatibilityClient) CanProvideModel(modelName string) bool {
	for _, model := range c.compatConfig.Models {
		if model.Alias == modelName {
			return true
		}
	}
	return false
}

// GetUserAgent returns the user agent string for OpenAI compatibility API requests.
func (c *OpenAICompatibilityClient) GetUserAgent() string {
	return fmt.Sprintf("cli-proxy-api-%s", c.compatConfig.Name)
}

// TokenStorage returns nil as this client doesn't use traditional token storage.
func (c *OpenAICompatibilityClient) TokenStorage() auth.TokenStorage {
	return nil
}

// GetCurrentAPIKey returns the current API key to use, with rotation support.
func (c *OpenAICompatibilityClient) GetCurrentAPIKey() string {
	if len(c.compatConfig.APIKeys) == 0 {
		return ""
	}

	key := c.compatConfig.APIKeys[c.currentAPIKeyIndex]
	// Rotate to next key for load balancing
	c.currentAPIKeyIndex = (c.currentAPIKeyIndex + 1) % len(c.compatConfig.APIKeys)
	return key
}

// GetActualModelName returns the actual model name to use with the external API
// based on the provided alias.
func (c *OpenAICompatibilityClient) GetActualModelName(alias string) string {
	for _, model := range c.compatConfig.Models {
		if model.Alias == alias {
			return model.Name
		}
	}
	return alias // fallback to alias if not found
}

// APIRequest makes an HTTP request to the OpenAI-compatible API.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The model name to use.
//   - endpoint: The API endpoint path.
//   - rawJSON: The raw JSON request data.
//   - alt: Alternative response format (not used for OpenAI compatibility).
//   - stream: Whether this is a streaming request.
//
// Returns:
//   - io.ReadCloser: The response body reader.
//   - *interfaces.ErrorMessage: An error message if the request fails.
func (c *OpenAICompatibilityClient) APIRequest(ctx context.Context, modelName string, endpoint string, rawJSON []byte, alt string, stream bool) (io.ReadCloser, *interfaces.ErrorMessage) {
	// Replace the model alias with the actual model name in the request
	actualModelName := c.GetActualModelName(modelName)
	modifiedJSON, errReplace := sjson.SetBytes(rawJSON, "model", actualModelName)
	if errReplace != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("failed to replace model name: %w", errReplace),
		}
	}

	// Create the HTTP request
	url := strings.TrimSuffix(c.compatConfig.BaseURL, "/") + endpoint
	req, errReq := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(modifiedJSON))
	if errReq != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("failed to create request: %w", errReq),
		}
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	apiKey := c.GetCurrentAPIKey()
	if apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	}
	req.Header.Set("User-Agent", c.GetUserAgent())

	if stream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
	}

	log.Debugf("OpenAI Compatibility [%s] API request: %s", c.compatConfig.Name, util.HideAPIKey(apiKey))

	if c.cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			ginContext.Set("API_REQUEST", modifiedJSON)
		}
	}

	// Send the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to execute request: %v", err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			if err = resp.Body.Close(); err != nil {
				log.Printf("warn: failed to close response body: %v", err)
			}
		}()
		bodyBytes, _ := io.ReadAll(resp.Body)
		// log.Debug(string(jsonBody))
		return nil, &interfaces.ErrorMessage{StatusCode: resp.StatusCode, Error: fmt.Errorf("%s", string(bodyBytes))}
	}

	return resp.Body, nil
}

// SendRawMessage sends a raw message to the OpenAI-compatible API.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The model alias name to use.
//   - rawJSON: The raw JSON request data.
//   - alt: Alternative response format parameter.
//
// Returns:
//   - []byte: The response data from the API.
//   - *interfaces.ErrorMessage: An error message if the request fails.
func (c *OpenAICompatibilityClient) SendRawMessage(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	originalRequestRawJSON := bytes.Clone(rawJSON)

	handler := ctx.Value("handler").(interfaces.APIHandler)
	handlerType := handler.HandlerType()
	rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, false)

	respBody, err := c.APIRequest(ctx, modelName, "/chat/completions", rawJSON, alt, false)
	if err != nil {
		if err.StatusCode == 429 {
			now := time.Now()
			c.modelQuotaExceeded[modelName] = &now
			// Update model registry quota status
			c.SetModelQuotaExceeded(modelName)
		}
		return nil, err
	}
	delete(c.modelQuotaExceeded, modelName)
	// Clear quota status in model registry
	c.ClearModelQuotaExceeded(modelName)
	bodyBytes, errReadAll := io.ReadAll(respBody)
	if errReadAll != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: errReadAll}
	}

	_ = respBody.Close()
	c.AddAPIResponseData(ctx, bodyBytes)

	var param any
	bodyBytes = []byte(translator.ResponseNonStream(handlerType, c.Type(), ctx, modelName, originalRequestRawJSON, rawJSON, bodyBytes, &param))

	return bodyBytes, nil
}

// SendRawMessageStream sends a raw streaming message to the OpenAI-compatible API.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The model alias name to use.
//   - rawJSON: The raw JSON request data.
//   - alt: Alternative response format parameter.
//
// Returns:
//   - <-chan []byte: A channel that will receive response chunks.
//   - <-chan *interfaces.ErrorMessage: A channel that will receive error messages.
func (c *OpenAICompatibilityClient) SendRawMessageStream(ctx context.Context, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	originalRequestRawJSON := bytes.Clone(rawJSON)

	handler := ctx.Value("handler").(interfaces.APIHandler)
	handlerType := handler.HandlerType()
	rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, true)

	dataTag := []byte("data: ")
	dataUglyTag := []byte("data:") // Some APIs providers don't add space after "data:", fuck for them all
	doneTag := []byte("data: [DONE]")
	errChan := make(chan *interfaces.ErrorMessage)
	dataChan := make(chan []byte)
	// log.Debugf(string(rawJSON))
	// return dataChan, errChan
	go func() {
		defer close(errChan)
		defer close(dataChan)

		// Set streaming flag in the request
		rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)

		newCtx := context.WithValue(ctx, "gin", ctx.Value("gin").(*gin.Context))

		stream, err := c.APIRequest(newCtx, modelName, "/chat/completions", rawJSON, alt, true)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				// Update model registry quota status
				c.SetModelQuotaExceeded(modelName)
			}
			errChan <- err
			return
		}
		delete(c.modelQuotaExceeded, modelName)
		// Clear quota status in model registry
		c.ClearModelQuotaExceeded(modelName)
		defer func() {
			_ = stream.Close()
		}()

		scanner := bufio.NewScanner(stream)

		if translator.NeedConvert(handlerType, c.Type()) {
			var param any
			for scanner.Scan() {
				line := scanner.Bytes()
				if bytes.HasPrefix(line, dataTag) {
					if bytes.Equal(line, doneTag) {
						break
					}
					lines := translator.Response(handlerType, c.Type(), newCtx, modelName, originalRequestRawJSON, rawJSON, line[6:], &param)
					for i := 0; i < len(lines); i++ {
						c.AddAPIResponseData(ctx, line)
						dataChan <- []byte(lines[i])
					}
				} else if bytes.HasPrefix(line, dataUglyTag) {
					if bytes.Equal(line, doneTag) {
						break
					}
					lines := translator.Response(handlerType, c.Type(), newCtx, modelName, originalRequestRawJSON, rawJSON, line[5:], &param)
					for i := 0; i < len(lines); i++ {
						c.AddAPIResponseData(ctx, line)
						dataChan <- []byte(lines[i])
					}
				} else if bytes.HasPrefix(line, dataUglyTag) {
					if bytes.Equal(line, doneTag) {
						break
					}
					lines := translator.Response(handlerType, c.Type(), newCtx, modelName, line[5:], &param)
					for i := 0; i < len(lines); i++ {
						dataChan <- []byte(lines[i])
					}
				} else if bytes.HasPrefix(line, dataUglyTag) {
					if bytes.Equal(line, doneTag) {
						break
					}
					lines := translator.Response(handlerType, c.Type(), newCtx, modelName, line[5:], &param)
					for i := 0; i < len(lines); i++ {
						dataChan <- []byte(lines[i])
					}
				} else if bytes.HasPrefix(line, dataUglyTag) {
					if bytes.Equal(line, doneTag) {
						break
					}
					lines := translator.Response(handlerType, c.Type(), newCtx, modelName, line[5:], &param)
					for i := 0; i < len(lines); i++ {
						dataChan <- []byte(lines[i])
					}
				}
			}
		} else {
			// No translation needed, stream data directly
			for scanner.Scan() {
				line := scanner.Bytes()
				if bytes.HasPrefix(line, dataTag) {
					if bytes.Equal(line, doneTag) {
						break
					}
					c.AddAPIResponseData(newCtx, line[6:])
					dataChan <- line[6:]
				} else if bytes.HasPrefix(line, dataUglyTag) {
					c.AddAPIResponseData(newCtx, line[5:])
					dataChan <- line[5:]
				}
			}
		}

		if scanner.Err() != nil {
			errChan <- &interfaces.ErrorMessage{StatusCode: 500, Error: scanner.Err()}
		}
	}()

	return dataChan, errChan
}

// SendRawTokenCount sends a token count request (not implemented for OpenAI compatibility).
// This method is required by the Client interface but not supported by OpenAI compatibility clients.
func (c *OpenAICompatibilityClient) SendRawTokenCount(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	return nil, &interfaces.ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("token counting not supported for OpenAI compatibility clients"),
	}
}

// GetEmail returns a placeholder email for this OpenAI compatibility client.
// Since these clients don't use traditional email-based authentication,
// we return the provider name as an identifier.
func (c *OpenAICompatibilityClient) GetEmail() string {
	return fmt.Sprintf("openai-compatibility-%s", c.compatConfig.Name)
}

// IsModelQuotaExceeded checks if the specified model has exceeded its quota.
// For OpenAI compatibility clients, this is based on tracked quota exceeded times.
func (c *OpenAICompatibilityClient) IsModelQuotaExceeded(model string) bool {
	if quota, exists := c.modelQuotaExceeded[model]; exists && quota != nil {
		// Check if quota exceeded time is less than 5 minutes ago
		if time.Since(*quota) < 5*time.Minute {
			return true
		}
		// Clear expired quota tracking
		delete(c.modelQuotaExceeded, model)
	}
	return false
}

// SaveTokenToFile returns nil as this client type doesn't use traditional token storage.
func (c *OpenAICompatibilityClient) SaveTokenToFile() error {
	// No token file to save for OpenAI compatibility clients
	return nil
}

// RefreshTokens is not applicable for OpenAI compatibility clients as they use API keys.
func (c *OpenAICompatibilityClient) RefreshTokens(ctx context.Context) error {
	// API keys don't need refreshing
	return nil
}

// GetRequestMutex returns the mutex used to synchronize requests for this client.
// This ensures that only one request is processed at a time for quota management.
//
// Returns:
//   - *sync.Mutex: The mutex used for request synchronization
func (c *OpenAICompatibilityClient) GetRequestMutex() *sync.Mutex {
	return nil
}
