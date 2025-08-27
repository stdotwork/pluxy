// Package handlers provides core API handler functionality for the CLI Proxy API server.
// It includes common types, client management, load balancing, and error handling
// shared across all API endpoint handlers (OpenAI, Claude, Gemini).
package handlers

import (
	"fmt"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

// ErrorResponse represents a standard error response format for the API.
// It contains a single ErrorDetail field.
type ErrorResponse struct {
	// Error contains detailed information about the error that occurred.
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides specific information about an error that occurred.
// It includes a human-readable message, an error type, and an optional error code.
type ErrorDetail struct {
	// Message is a human-readable message providing more details about the error.
	Message string `json:"message"`

	// Type is the category of error that occurred (e.g., "invalid_request_error").
	Type string `json:"type"`

	// Code is a short code identifying the error, if applicable.
	Code string `json:"code,omitempty"`
}

// BaseAPIHandler contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service and manages
// load balancing, client selection, and configuration.
type BaseAPIHandler struct {
	// CliClients is the pool of available AI service clients.
	CliClients []interfaces.Client

	// Cfg holds the current application configuration.
	Cfg *config.Config

	// Mutex ensures thread-safe access to shared resources.
	Mutex *sync.Mutex

	// LastUsedClientIndex tracks the last used client index for each provider
	// to implement round-robin load balancing.
	LastUsedClientIndex map[string]int
}

// NewBaseAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and configuration as input.
//
// Parameters:
//   - cliClients: A slice of AI service clients
//   - cfg: The application configuration
//
// Returns:
//   - *BaseAPIHandler: A new API handlers instance
func NewBaseAPIHandlers(cliClients []interfaces.Client, cfg *config.Config) *BaseAPIHandler {
	return &BaseAPIHandler{
		CliClients:          cliClients,
		Cfg:                 cfg,
		Mutex:               &sync.Mutex{},
		LastUsedClientIndex: make(map[string]int),
	}
}

// UpdateClients updates the handlers' client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (h *BaseAPIHandler) UpdateClients(clients []interfaces.Client, cfg *config.Config) {
	h.CliClients = clients
	h.Cfg = cfg
}

// GetClient returns an available client from the pool using round-robin load balancing.
// It checks for quota limits and tries to find an unlocked client for immediate use.
// The modelName parameter is used to check quota status for specific models.
//
// Parameters:
//   - modelName: The name of the model to be used
//   - isGenerateContent: Optional parameter to indicate if this is for content generation
//
// Returns:
//   - client.Client: An available client for the requested model
//   - *client.ErrorMessage: An error message if no client is available
func (h *BaseAPIHandler) GetClient(modelName string, isGenerateContent ...bool) (interfaces.Client, *interfaces.ErrorMessage) {
	clients := make([]interfaces.Client, 0)
	for i := 0; i < len(h.CliClients); i++ {
		if h.CliClients[i].CanProvideModel(modelName) {
			clients = append(clients, h.CliClients[i])
		}
	}

	// Lock the mutex to update the last used client index
	h.Mutex.Lock()
	if _, hasKey := h.LastUsedClientIndex[modelName]; !hasKey {
		h.LastUsedClientIndex[modelName] = 0
	}

	if len(clients) == 0 {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("no clients available")}
	}

	var cliClient interfaces.Client

	startIndex := h.LastUsedClientIndex[modelName]
	if (len(isGenerateContent) > 0 && isGenerateContent[0]) || len(isGenerateContent) == 0 {
		currentIndex := (startIndex + 1) % len(clients)
		h.LastUsedClientIndex[modelName] = currentIndex
	}
	h.Mutex.Unlock()

	// Reorder the client to start from the last used index
	reorderedClients := make([]interfaces.Client, 0)
	for i := 0; i < len(clients); i++ {
		cliClient = clients[(startIndex+1+i)%len(clients)]
		if cliClient.IsModelQuotaExceeded(modelName) {
			if cliClient.Provider() == "gemini-cli" {
				log.Debugf("Gemini Model %s is quota exceeded for account %s, project id: %s", modelName, cliClient.GetEmail(), cliClient.(*client.GeminiCLIClient).GetProjectID())
			} else if cliClient.Provider() == "gemini" {
				log.Debugf("Gemini Model %s is quota exceeded for account %s", modelName, cliClient.GetEmail())
			} else if cliClient.Provider() == "codex" {
				log.Debugf("Codex Model %s is quota exceeded for account %s", modelName, cliClient.GetEmail())
			} else if cliClient.Provider() == "claude" {
				log.Debugf("Claude Model %s is quota exceeded for account %s", modelName, cliClient.GetEmail())
			} else if cliClient.Provider() == "qwen" {
				log.Debugf("Qwen Model %s is quota exceeded for account %s", modelName, cliClient.GetEmail())
			} else if cliClient.Type() == "openai-compatibility" {
				log.Debugf("OpenAI Compatibility Model %s is quota exceeded for provider %s", modelName, cliClient.Provider())
			}
			cliClient = nil
			continue

		}
		reorderedClients = append(reorderedClients, cliClient)
	}

	if len(reorderedClients) == 0 {
		if util.GetProviderName(modelName, h.Cfg) == "claude" {
			// log.Debugf("Claude Model %s is quota exceeded for all accounts", modelName)
			return nil, &interfaces.ErrorMessage{StatusCode: 429, Error: fmt.Errorf(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`)}
		}
		return nil, &interfaces.ErrorMessage{StatusCode: 429, Error: fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName)}
	}

	locked := false
	for i := 0; i < len(reorderedClients); i++ {
		cliClient = reorderedClients[i]
		if mutex := cliClient.GetRequestMutex(); mutex != nil {
			if mutex.TryLock() {
				locked = true
				break
			}
		} else {
			locked = true
		}
	}
	if !locked {
		cliClient = clients[0]
		if mutex := cliClient.GetRequestMutex(); mutex != nil {
			mutex.Lock()
		}
	}

	return cliClient, nil
}

// GetAlt extracts the 'alt' parameter from the request query string.
// It checks both 'alt' and '$alt' parameters and returns the appropriate value.
//
// Parameters:
//   - c: The Gin context containing the HTTP request
//
// Returns:
//   - string: The alt parameter value, or empty string if it's "sse"
func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	var alt string
	var hasAlt bool
	alt, hasAlt = c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

// GetContextWithCancel creates a new context with cancellation capabilities.
// It embeds the Gin context and the API handler into the new context for later use.
// The returned cancel function also handles logging the API response if request logging is enabled.
//
// Parameters:
//   - handler: The API handler associated with the request.
//   - c: The Gin context of the current request.
//   - ctx: The parent context.
//
// Returns:
//   - context.Context: The new context with cancellation and embedded values.
//   - APIHandlerCancelFunc: A function to cancel the context and log the response.
func (h *BaseAPIHandler) GetContextWithCancel(handler interfaces.APIHandler, c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	newCtx, cancel := context.WithCancel(ctx)
	newCtx = context.WithValue(newCtx, "gin", c)
	newCtx = context.WithValue(newCtx, "handler", handler)
	return newCtx, func(params ...interface{}) {
		if h.Cfg.RequestLog {
			if len(params) == 1 {
				data := params[0]
				switch data.(type) {
				case []byte:
					c.Set("API_RESPONSE", data.([]byte))
				case error:
					c.Set("API_RESPONSE", []byte(data.(error).Error()))
				case string:
					c.Set("API_RESPONSE", []byte(data.(string)))
				case bool:
				case nil:
				}
			}
		}

		cancel()
	}
}

// APIHandlerCancelFunc is a function type for canceling an API handler's context.
// It can optionally accept parameters, which are used for logging the response.
type APIHandlerCancelFunc func(params ...interface{})
