// Package handlers provides core API handler functionality for the CLI Proxy API server.
// It includes common types, client management, load balancing, and error handling
// shared across all API endpoint handlers (OpenAI, Claude, Gemini).
package handlers

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"sync"
)

// ErrorResponse represents a standard error response format for the API.
// It contains a single ErrorDetail field.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides specific information about an error that occurred.
// It includes a human-readable message, an error type, and an optional error code.
type ErrorDetail struct {
	// A human-readable message providing more details about the error.
	Message string `json:"message"`
	// The type of error that occurred (e.g., "invalid_request_error").
	Type string `json:"type"`
	// A short code identifying the error, if applicable.
	Code string `json:"code,omitempty"`
}

// APIHandlers contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service.
type APIHandlers struct {
	CliClients          []*client.Client
	Cfg                 *config.Config
	Mutex               *sync.Mutex
	LastUsedClientIndex int
}

// NewAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and a debug flag as input.
func NewAPIHandlers(cliClients []*client.Client, cfg *config.Config) *APIHandlers {
	return &APIHandlers{
		CliClients:          cliClients,
		Cfg:                 cfg,
		Mutex:               &sync.Mutex{},
		LastUsedClientIndex: 0,
	}
}

// UpdateClients updates the handlers' client list and configuration
func (h *APIHandlers) UpdateClients(clients []*client.Client, cfg *config.Config) {
	h.CliClients = clients
	h.Cfg = cfg
}

// GetClient returns an available client from the pool using round-robin load balancing.
// It checks for quota limits and tries to find an unlocked client for immediate use.
// The modelName parameter is used to check quota status for specific models.
func (h *APIHandlers) GetClient(modelName string, isGenerateContent ...bool) (*client.Client, *client.ErrorMessage) {
	if len(h.CliClients) == 0 {
		return nil, &client.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("no clients available")}
	}

	var cliClient *client.Client

	// Lock the mutex to update the last used client index
	h.Mutex.Lock()
	startIndex := h.LastUsedClientIndex
	if (len(isGenerateContent) > 0 && isGenerateContent[0]) || len(isGenerateContent) == 0 {
		currentIndex := (startIndex + 1) % len(h.CliClients)
		h.LastUsedClientIndex = currentIndex
	}
	h.Mutex.Unlock()

	// Reorder the client to start from the last used index
	reorderedClients := make([]*client.Client, 0)
	for i := 0; i < len(h.CliClients); i++ {
		cliClient = h.CliClients[(startIndex+1+i)%len(h.CliClients)]
		if cliClient.IsModelQuotaExceeded(modelName) {
			log.Debugf("Model %s is quota exceeded for account %s, project id: %s", modelName, cliClient.GetEmail(), cliClient.GetProjectID())
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
		if cliClient.RequestMutex.TryLock() {
			locked = true
			break
		}
	}
	if !locked {
		cliClient = h.CliClients[0]
		cliClient.RequestMutex.Lock()
	}

	return cliClient, nil
}

// GetAlt extracts the 'alt' parameter from the request query string.
// It checks both 'alt' and '$alt' parameters and returns the appropriate value.
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
