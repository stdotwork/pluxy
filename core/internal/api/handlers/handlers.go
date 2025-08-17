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

// APIHandlers contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service and manages
// load balancing, client selection, and configuration.
type APIHandlers struct {
	// CliClients is the pool of available AI service clients.
	CliClients []client.Client

	// Cfg holds the current application configuration.
	Cfg *config.Config

	// Mutex ensures thread-safe access to shared resources.
	Mutex *sync.Mutex

	// LastUsedClientIndex tracks the last used client index for each provider
	// to implement round-robin load balancing.
	LastUsedClientIndex map[string]int

	// apiResponseData recording provider api response data
	apiResponseData map[*gin.Context][]byte
}

// NewAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and configuration as input.
//
// Parameters:
//   - cliClients: A slice of AI service clients
//   - cfg: The application configuration
//
// Returns:
//   - *APIHandlers: A new API handlers instance
func NewAPIHandlers(cliClients []client.Client, cfg *config.Config) *APIHandlers {
	return &APIHandlers{
		CliClients:          cliClients,
		Cfg:                 cfg,
		Mutex:               &sync.Mutex{},
		LastUsedClientIndex: make(map[string]int),
		apiResponseData:     make(map[*gin.Context][]byte),
	}
}

// UpdateClients updates the handlers' client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (h *APIHandlers) UpdateClients(clients []client.Client, cfg *config.Config) {
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
func (h *APIHandlers) GetClient(modelName string, isGenerateContent ...bool) (client.Client, *client.ErrorMessage) {
	provider := util.GetProviderName(modelName)
	clients := make([]client.Client, 0)
	if provider == "gemini" {
		for i := 0; i < len(h.CliClients); i++ {
			if cli, ok := h.CliClients[i].(*client.GeminiClient); ok {
				clients = append(clients, cli)
			}
		}
	} else if provider == "gpt" {
		for i := 0; i < len(h.CliClients); i++ {
			if cli, ok := h.CliClients[i].(*client.CodexClient); ok {
				clients = append(clients, cli)
			}
		}
	}

	if _, hasKey := h.LastUsedClientIndex[provider]; !hasKey {
		h.LastUsedClientIndex[provider] = 0
	}

	if len(clients) == 0 {
		return nil, &client.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("no clients available")}
	}

	var cliClient client.Client

	// Lock the mutex to update the last used client index
	h.Mutex.Lock()
	startIndex := h.LastUsedClientIndex[provider]
	if (len(isGenerateContent) > 0 && isGenerateContent[0]) || len(isGenerateContent) == 0 {
		currentIndex := (startIndex + 1) % len(clients)
		h.LastUsedClientIndex[provider] = currentIndex
	}
	h.Mutex.Unlock()

	// Reorder the client to start from the last used index
	reorderedClients := make([]client.Client, 0)
	for i := 0; i < len(clients); i++ {
		cliClient = clients[(startIndex+1+i)%len(clients)]
		if cliClient.IsModelQuotaExceeded(modelName) {
			if provider == "gemini" {
				log.Debugf("Gemini Model %s is quota exceeded for account %s, project id: %s", modelName, cliClient.GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
			} else if provider == "gpt" {
				log.Debugf("Codex Model %s is quota exceeded for account %s", modelName, cliClient.GetEmail())
			}
			cliClient = nil
			continue

		}
		reorderedClients = append(reorderedClients, cliClient)
	}

	if len(reorderedClients) == 0 {
		return nil, &client.ErrorMessage{StatusCode: 429, Error: fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName)}
	}

	locked := false
	for i := 0; i < len(reorderedClients); i++ {
		cliClient = reorderedClients[i]
		if cliClient.GetRequestMutex().TryLock() {
			locked = true
			break
		}
	}
	if !locked {
		cliClient = clients[0]
		cliClient.GetRequestMutex().Lock()
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
func (h *APIHandlers) GetAlt(c *gin.Context) string {
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

func (h *APIHandlers) GetContextWithCancel(c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	newCtx, cancel := context.WithCancel(ctx)
	newCtx = context.WithValue(newCtx, "gin", c)
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
			} else {
				if _, hasKey := h.apiResponseData[c]; hasKey {
					c.Set("API_RESPONSE", h.apiResponseData[c])
					delete(h.apiResponseData, c)
				}
			}
		}

		cancel()
	}
}

func (h *APIHandlers) AddAPIResponseData(c *gin.Context, data []byte) {
	if h.Cfg.RequestLog {
		if _, hasKey := h.apiResponseData[c]; !hasKey {
			h.apiResponseData[c] = make([]byte, 0)
		}
		h.apiResponseData[c] = append(h.apiResponseData[c], data...)
	}
}

type APIHandlerCancelFunc func(params ...interface{})
