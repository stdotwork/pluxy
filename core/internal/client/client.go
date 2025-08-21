// Package client defines the interface and base structure for AI API clients.
// It provides a common interface that all supported AI service clients must implement,
// including methods for sending messages, handling streams, and managing authentication.
package client

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/config"
)

// ClientBase provides a common base structure for all AI API clients.
// It implements shared functionality such as request synchronization, HTTP client management,
// configuration access, token storage, and quota tracking.
type ClientBase struct {
	// RequestMutex ensures only one request is processed at a time for quota management.
	RequestMutex *sync.Mutex

	// httpClient is the HTTP client used for making API requests.
	httpClient *http.Client

	// cfg holds the application configuration.
	cfg *config.Config

	// tokenStorage manages authentication tokens for the client.
	tokenStorage auth.TokenStorage

	// modelQuotaExceeded tracks when models have exceeded their quota.
	// The map key is the model name, and the value is the time when the quota was exceeded.
	modelQuotaExceeded map[string]*time.Time
}

// GetRequestMutex returns the mutex used to synchronize requests for this client.
// This ensures that only one request is processed at a time for quota management.
//
// Returns:
//   - *sync.Mutex: The mutex used for request synchronization
func (c *ClientBase) GetRequestMutex() *sync.Mutex {
	return c.RequestMutex
}

// AddAPIResponseData adds API response data to the Gin context for logging purposes.
// This method appends the provided data to any existing response data in the context,
// or creates a new entry if none exists. It only performs this operation if request
// logging is enabled in the configuration.
//
// Parameters:
//   - ctx: The context for the request
//   - line: The response data to be added
func (c *ClientBase) AddAPIResponseData(ctx context.Context, line []byte) {
	if c.cfg.RequestLog {
		data := bytes.TrimSpace(bytes.Clone(line))
		if ginContext, ok := ctx.Value("gin").(*gin.Context); len(data) > 0 && ok {
			if apiResponseData, isExist := ginContext.Get("API_RESPONSE"); isExist {
				if byteAPIResponseData, isOk := apiResponseData.([]byte); isOk {
					// Append new data and separator to existing response data
					byteAPIResponseData = append(byteAPIResponseData, data...)
					byteAPIResponseData = append(byteAPIResponseData, []byte("\n\n")...)
					ginContext.Set("API_RESPONSE", byteAPIResponseData)
				}
			} else {
				// Create new response data entry
				ginContext.Set("API_RESPONSE", data)
			}
		}
	}
}
