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
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/registry"
	"github.com/luispater/CLIProxyAPI/internal/util"
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

// ChatCompletions handles the /v1/chat/completions endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) ChatCompletions(c *gin.Context) {
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

// Completions handles the /v1/completions endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
// This endpoint follows the OpenAIResponses completions API specification.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Completions(c *gin.Context) {
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
		h.handleCompletionsStreamingResponse(c, rawJSON)
	} else {
		h.handleCompletionsNonStreamingResponse(c, rawJSON)
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

	retryCount := 0
	for retryCount <= h.Cfg.RequestRetry {
		var errorResponse *interfaces.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		resp, err := cliClient.SendRawMessage(cliCtx, modelName, rawJSON, "")
		if err != nil {
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
				}
				retryCount++
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
			cliCancel(resp)
			break
		}
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

	retryCount := 0
outLoop:
	for retryCount <= h.Cfg.RequestRetry {
		var errorResponse *interfaces.ErrorMessage
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
					// Stream is closed, send the final [DONE] message.
					_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
					flusher.Flush()
					cliCancel()
					return
				}

				_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunk))
				flusher.Flush()
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
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
}

// handleCompletionsNonStreamingResponse handles non-streaming completions responses.
// It converts completions request to chat completions format, sends to backend,
// then converts the response back to completions format before sending to client.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible completions request
func (h *OpenAIResponsesAPIHandler) handleCompletionsNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	// Convert completions request to chat completions format
	chatCompletionsJSON := convertCompletionsRequestToChatCompletions(rawJSON)

	modelName := gjson.GetBytes(chatCompletionsJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	var cliClient interfaces.Client
	defer func() {
		if cliClient != nil {
			if mutex := cliClient.GetRequestMutex(); mutex != nil {
				mutex.Unlock()
			}
		}
	}()

	retryCount := 0
	for retryCount <= h.Cfg.RequestRetry {
		var errorResponse *interfaces.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		// Send the converted chat completions request
		resp, err := cliClient.SendRawMessage(cliCtx, modelName, chatCompletionsJSON, "")
		if err != nil {
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
			default:
				// Forward other errors directly to the client
				c.Status(err.StatusCode)
				_, _ = c.Writer.Write([]byte(err.Error.Error()))
				cliCancel(err.Error)
			}
			break
		} else {
			// Convert chat completions response back to completions format
			completionsResp := convertChatCompletionsResponseToCompletions(resp)
			_, _ = c.Writer.Write(completionsResp)
			cliCancel(completionsResp)
			break
		}
	}
}

// handleCompletionsStreamingResponse handles streaming completions responses.
// It converts completions request to chat completions format, streams from backend,
// then converts each response chunk back to completions format before sending to client.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible completions request
func (h *OpenAIResponsesAPIHandler) handleCompletionsStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Convert completions request to chat completions format
	chatCompletionsJSON := convertCompletionsRequestToChatCompletions(rawJSON)

	modelName := gjson.GetBytes(chatCompletionsJSON, "model").String()
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

	retryCount := 0
outLoop:
	for retryCount <= h.Cfg.RequestRetry {
		var errorResponse *interfaces.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			flusher.Flush()
			cliCancel()
			return
		}

		// Send the converted chat completions request and receive response chunks
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, modelName, chatCompletionsJSON, "")

		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("client disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					// Stream is closed, send the final [DONE] message.
					_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
					flusher.Flush()
					cliCancel()
					return
				}

				// Convert chat completions chunk to completions chunk format
				completionsChunk := convertChatCompletionsStreamChunkToCompletions(chunk)
				// Skip this chunk if it has no meaningful content (empty text)
				if completionsChunk != nil {
					_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(completionsChunk))
					flusher.Flush()
				}
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
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
}
