// Package claude provides HTTP handlers for Claude API code-related functionality.
// This package implements Claude-compatible streaming chat completions with sophisticated
// client rotation and quota management systems to ensure high availability and optimal
// resource utilization across multiple backend clients. It handles request translation
// between Claude API format and the underlying Gemini backend, providing seamless
// API compatibility while maintaining robust error handling and connection management.
package claude

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/v5/internal/api/handlers"
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/registry"
	"github.com/luispater/CLIProxyAPI/v5/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// ClaudeCodeAPIHandler contains the handlers for Claude API endpoints.
// It holds a pool of clients to interact with the backend service.
type ClaudeCodeAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewClaudeCodeAPIHandler creates a new Claude API handlers instance.
// It takes an BaseAPIHandler instance as input and returns a ClaudeCodeAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handler instance.
//
// Returns:
//   - *ClaudeCodeAPIHandler: A new Claude code API handler instance.
func NewClaudeCodeAPIHandler(apiHandlers *handlers.BaseAPIHandler) *ClaudeCodeAPIHandler {
	return &ClaudeCodeAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *ClaudeCodeAPIHandler) HandlerType() string {
	return CLAUDE
}

// Models returns a list of models supported by this handler.
func (h *ClaudeCodeAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("claude")
}

// ClaudeMessages handles Claude-compatible streaming chat completions.
// This function implements a sophisticated client rotation and quota management system
// to ensure high availability and optimal resource utilization across multiple backend clients.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeMessages(c *gin.Context) {
	// Extract raw JSON data from the incoming request
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if !streamResult.Exists() || streamResult.Type == gjson.False {
		return
	}

	h.handleStreamingResponse(c, rawJSON)
}

// ClaudeModels handles the Claude models listing endpoint.
// It returns a JSON response containing available Claude models and their specifications.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"data": h.Models(),
	})
}

// handleStreamingResponse streams Claude-compatible responses backed by Gemini.
// It sets up SSE, selects a backend client with rotation/quota logic,
// forwards chunks, and translates them to Claude CLI format.
//
// Parameters:
//   - c: The Gin context for the request.
//   - rawJSON: The raw JSON request body.
func (h *ClaudeCodeAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Set up Server-Sent Events (SSE) headers for streaming response
	// These headers are essential for maintaining a persistent connection
	// and enabling real-time streaming of chat completions
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	// Get the http.Flusher interface to manually flush the response.
	// This is crucial for streaming as it allows immediate sending of data chunks
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()

	// Create a cancellable context for the backend client request
	// This allows proper cleanup and cancellation of ongoing requests
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	var cliClient interfaces.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		// This prevents deadlocks and ensures proper resource cleanup
		if cliClient != nil {
			if mutex := cliClient.GetRequestMutex(); mutex != nil {
				mutex.Unlock()
			}
		}
	}()

	var errorResponse *interfaces.ErrorMessage
	retryCount := 0
	// Main client rotation loop with quota management
	// This loop implements a sophisticated load balancing and failover mechanism
outLoop:
	for retryCount <= h.Cfg.RequestRetry {
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			flusher.Flush()
			cliCancel()
			return
		}

		// Initiate streaming communication with the backend client using raw JSON
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, modelName, rawJSON, "")

		// Main streaming loop - handles multiple concurrent events using Go channels
		// This select statement manages four different types of events simultaneously
		for {
			select {
			// Case 1: Handle client disconnection
			// Detects when the HTTP client has disconnected and cleans up resources
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("claude client disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request to prevent resource leaks
					return
				}

			// Case 2: Process incoming response chunks from the backend
			// This handles the actual streaming data from the AI model
			case chunk, okStream := <-respChan:
				if !okStream {
					flusher.Flush()
					cliCancel()
					return
				}

				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n"))
			// Case 3: Handle errors from the backend
			// This manages various error conditions and implements retry logic
			case errInfo, okError := <-errChan:
				if okError {
					errorResponse = errInfo
					h.LoggingAPIResponseError(cliCtx, errInfo)
					// Special handling for quota exceeded errors
					// If configured, attempt to switch to a different project/client
					switch errInfo.StatusCode {
					case 429:
						if h.Cfg.QuotaExceeded.SwitchProject {
							log.Debugf("quota exceeded, switch client")
							continue outLoop // Restart the client selection process
						}
					case 403, 408, 500, 502, 503, 504:
						log.Debugf("http status code %d, switch client, %s", errInfo.StatusCode, util.HideAPIKey(cliClient.GetEmail()))
						retryCount++
						continue outLoop
					case 401:
						log.Debugf("unauthorized request, try to refresh token, %s", util.HideAPIKey(cliClient.GetEmail()))
						err := cliClient.RefreshTokens(cliCtx)
						if err != nil {
							log.Debugf("refresh token failed, switch client, %s", util.HideAPIKey(cliClient.GetEmail()))
						}
						retryCount++
						continue outLoop
					default:
						// Forward other errors directly to the client
						c.Status(errInfo.StatusCode)
						_, _ = fmt.Fprint(c.Writer, errInfo.Error.Error())
						flusher.Flush()
						cliCancel(errInfo.Error)
					}
					return
				}

			// Case 4: Send periodic keep-alive signals
			// Prevents connection timeouts during long-running requests
			case <-time.After(500 * time.Millisecond):
			}
		}
	}

	if errorResponse != nil {
		c.Status(errorResponse.StatusCode)
		_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
		flusher.Flush()
		cliCancel(errorResponse.Error)
		return
	}
}
