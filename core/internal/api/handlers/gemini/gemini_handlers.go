// Package gemini provides HTTP handlers for Gemini API endpoints.
// This package implements handlers for managing Gemini model operations including
// model listing, content generation, streaming content generation, and token counting.
// It serves as a proxy layer between clients and the Gemini backend service,
// handling request translation, client management, and response processing.
package gemini

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/registry"
	log "github.com/sirupsen/logrus"
)

// GeminiAPIHandler contains the handlers for Gemini API endpoints.
// It holds a pool of clients to interact with the backend service.
type GeminiAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewGeminiAPIHandler creates a new Gemini API handlers instance.
// It takes an BaseAPIHandler instance as input and returns a GeminiAPIHandler.
func NewGeminiAPIHandler(apiHandlers *handlers.BaseAPIHandler) *GeminiAPIHandler {
	return &GeminiAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *GeminiAPIHandler) HandlerType() string {
	return GEMINI
}

// Models returns the Gemini-compatible model metadata supported by this handler.
func (h *GeminiAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("gemini")
}

// GeminiModels handles the Gemini models listing endpoint.
// It returns a JSON response containing available Gemini models and their specifications.
func (h *GeminiAPIHandler) GeminiModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"models": h.Models(),
	})
}

// GeminiGetHandler handles GET requests for specific Gemini model information.
// It returns detailed information about a specific Gemini model based on the action parameter.
func (h *GeminiAPIHandler) GeminiGetHandler(c *gin.Context) {
	var request struct {
		Action string `uri:"action" binding:"required"`
	}
	if err := c.ShouldBindUri(&request); err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	switch request.Action {
	case "gemini-2.5-pro":
		c.JSON(http.StatusOK, gin.H{
			"name":             "models/gemini-2.5-pro",
			"version":          "2.5",
			"displayName":      "Gemini 2.5 Pro",
			"description":      "Stable release (June 17th, 2025) of Gemini 2.5 Pro",
			"inputTokenLimit":  1048576,
			"outputTokenLimit": 65536,
			"supportedGenerationMethods": []string{
				"generateContent",
				"countTokens",
				"createCachedContent",
				"batchGenerateContent",
			},
			"temperature":    1,
			"topP":           0.95,
			"topK":           64,
			"maxTemperature": 2,
			"thinking":       true,
		},
		)
	case "gemini-2.5-flash":
		c.JSON(http.StatusOK, gin.H{
			"name":             "models/gemini-2.5-flash",
			"version":          "001",
			"displayName":      "Gemini 2.5 Flash",
			"description":      "Stable version of Gemini 2.5 Flash, our mid-size multimodal model that supports up to 1 million tokens, released in June of 2025.",
			"inputTokenLimit":  1048576,
			"outputTokenLimit": 65536,
			"supportedGenerationMethods": []string{
				"generateContent",
				"countTokens",
				"createCachedContent",
				"batchGenerateContent",
			},
			"temperature":    1,
			"topP":           0.95,
			"topK":           64,
			"maxTemperature": 2,
			"thinking":       true,
		})
	case "gpt-5":
		c.JSON(http.StatusOK, gin.H{
			"name":             "gpt-5",
			"version":          "001",
			"displayName":      "GPT 5",
			"description":      "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
			"inputTokenLimit":  400000,
			"outputTokenLimit": 128000,
			"supportedGenerationMethods": []string{
				"generateContent",
			},
			"temperature":    1,
			"topP":           0.95,
			"topK":           64,
			"maxTemperature": 2,
			"thinking":       true,
		})
	default:
		c.JSON(http.StatusNotFound, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Not Found",
				Type:    "not_found",
			},
		})
	}
}

// GeminiHandler handles POST requests for Gemini API operations.
// It routes requests to appropriate handlers based on the action parameter (model:method format).
func (h *GeminiAPIHandler) GeminiHandler(c *gin.Context) {
	var request struct {
		Action string `uri:"action" binding:"required"`
	}
	if err := c.ShouldBindUri(&request); err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	action := strings.Split(request.Action, ":")
	if len(action) != 2 {
		c.JSON(http.StatusNotFound, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("%s not found.", c.Request.URL.Path),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	method := action[1]
	rawJSON, _ := c.GetRawData()

	switch method {
	case "generateContent":
		h.handleGenerateContent(c, action[0], rawJSON)
	case "streamGenerateContent":
		h.handleStreamGenerateContent(c, action[0], rawJSON)
	case "countTokens":
		h.handleCountTokens(c, action[0], rawJSON)
	}
}

// handleStreamGenerateContent handles streaming content generation requests for Gemini models.
// This function establishes a Server-Sent Events connection and streams the generated content
// back to the client in real-time. It supports both SSE format and direct streaming based
// on the 'alt' query parameter.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The name of the Gemini model to use for content generation
//   - rawJSON: The raw JSON request body containing generation parameters
func (h *GeminiAPIHandler) handleStreamGenerateContent(c *gin.Context, modelName string, rawJSON []byte) {
	alt := h.GetAlt(c)

	if alt == "" {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}

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
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, modelName, rawJSON, alt)
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("gemini client disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					cliCancel()
					return
				}

				if alt == "" {
					_, _ = c.Writer.Write([]byte("data: "))
					_, _ = c.Writer.Write(chunk)
					_, _ = c.Writer.Write([]byte("\n\n"))
				} else {
					_, _ = c.Writer.Write(chunk)
				}
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

// handleCountTokens handles token counting requests for Gemini models.
// This function counts the number of tokens in the provided content without
// generating a response. It's useful for quota management and content validation.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The name of the Gemini model to use for token counting
//   - rawJSON: The raw JSON request body containing the content to count
func (h *GeminiAPIHandler) handleCountTokens(c *gin.Context, modelName string, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	var cliClient interfaces.Client
	defer func() {
		if cliClient != nil {
			if mutex := cliClient.GetRequestMutex(); mutex != nil {
				mutex.Unlock()
			}
		}
	}()

	for {
		var errorResponse *interfaces.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName, false)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		resp, err := cliClient.SendRawTokenCount(cliCtx, modelName, rawJSON, alt)
		if err != nil {
			if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
				continue
			} else {
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

// handleGenerateContent handles non-streaming content generation requests for Gemini models.
// This function processes the request synchronously and returns the complete generated
// response in a single API call. It supports various generation parameters and
// response formats.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The name of the Gemini model to use for content generation
//   - rawJSON: The raw JSON request body containing generation parameters and content
func (h *GeminiAPIHandler) handleGenerateContent(c *gin.Context, modelName string, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)

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

		resp, err := cliClient.SendRawMessage(cliCtx, modelName, rawJSON, alt)
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
			_, _ = c.Writer.Write(resp)
			cliCancel(resp)
			break
		}
	}
}
