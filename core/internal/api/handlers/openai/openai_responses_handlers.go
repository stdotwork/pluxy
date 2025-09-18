// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

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

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OPENAI_RESPONSE
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.Models(),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
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
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	var cliClient interfaces.Client
	defer func() {
		if cliClient != nil {
			if mutex := cliClient.GetRequestMutex(); mutex != nil {
				mutex.Unlock()
			}
		}
	}()

	var errorResponse *interfaces.ErrorMessage
	retryCount := 0
	for retryCount <= h.Cfg.RequestRetry {
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		resp, err := cliClient.SendRawMessage(cliCtx, modelName, rawJSON, "")
		if err != nil {
			errorResponse = err
			h.LoggingAPIResponseError(cliCtx, err)

			switch err.StatusCode {
			case 429:
				if h.Cfg.QuotaExceeded.SwitchProject {
					log.Debugf("quota exceeded, switch client")
					continue // Restart the client selection process
				}
			case 403, 408, 500, 502, 503, 504:
				log.Debugf("http status code %d, switch client", err.StatusCode)
				retryCount++
				continue
			case 401:
				log.Debugf("unauthorized request, try to refresh token, %s", util.HideAPIKey(cliClient.GetEmail()))
				errRefreshTokens := cliClient.RefreshTokens(cliCtx)
				if errRefreshTokens != nil {
					log.Debugf("refresh token failed, switch client, %s", util.HideAPIKey(cliClient.GetEmail()))
					cliClient.SetUnavailable()
				}
				retryCount++
				continue
			case 402:
				cliClient.SetUnavailable()
				continue
			default:
				// Forward other errors directly to the client
				c.Status(err.StatusCode)
				_, _ = c.Writer.Write([]byte(err.Error.Error()))
				cliCancel(err.Error)
			}
			break
		} else {
			_, _ = c.Writer.Write(resp)
			cliCancel()
			break
		}
	}
	if errorResponse != nil {
		c.Status(errorResponse.StatusCode)
		_, _ = c.Writer.Write([]byte(errorResponse.Error.Error()))
		cliCancel(errorResponse.Error)
		return
	}

}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	// Get the http.Flusher interface to manually flush the response.
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
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	var cliClient interfaces.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		if cliClient != nil {
			if mutex := cliClient.GetRequestMutex(); mutex != nil {
				mutex.Unlock()
			}
		}
	}()

	var errorResponse *interfaces.ErrorMessage
	retryCount := 0
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

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, modelName, rawJSON, "")

		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("openai client disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					flusher.Flush()
					cliCancel()
					return
				}

				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					errorResponse = err
					h.LoggingAPIResponseError(cliCtx, err)
					switch err.StatusCode {
					case 429:
						if h.Cfg.QuotaExceeded.SwitchProject {
							log.Debugf("quota exceeded, switch client")
							continue outLoop // Restart the client selection process
						}
					case 403, 408, 500, 502, 503, 504:
						log.Debugf("http status code %d, switch client", err.StatusCode)
						retryCount++
						continue outLoop
					case 401:
						log.Debugf("unauthorized request, try to refresh token, %s", util.HideAPIKey(cliClient.GetEmail()))
						errRefreshTokens := cliClient.RefreshTokens(cliCtx)
						if errRefreshTokens != nil {
							log.Debugf("refresh token failed, switch client, %s", util.HideAPIKey(cliClient.GetEmail()))
							cliClient.SetUnavailable()
						}
						retryCount++
						continue outLoop
					case 402:
						cliClient.SetUnavailable()
						continue outLoop
					default:
						// Forward other errors directly to the client
						c.Status(err.StatusCode)
						_, _ = fmt.Fprint(c.Writer, err.Error.Error())
						flusher.Flush()
						cliCancel(err.Error)
					}
					return
				}
			// Send a keep-alive signal to the client.
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
