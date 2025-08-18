// Package claude provides HTTP handlers for Claude API code-related functionality.
// This package implements Claude-compatible streaming chat completions with sophisticated
// client rotation and quota management systems to ensure high availability and optimal
// resource utilization across multiple backend clients. It handles request translation
// between Claude API format and the underlying Gemini backend, providing seamless
// API compatibility while maintaining robust error handling and connection management.
package claude

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	"github.com/luispater/CLIProxyAPI/internal/client"
	translatorClaudeCodeToCodex "github.com/luispater/CLIProxyAPI/internal/translator/codex/claude/code"
	translatorClaudeCodeToGeminiCli "github.com/luispater/CLIProxyAPI/internal/translator/gemini-cli/claude/code"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeCodeAPIHandlers contains the handlers for Claude API endpoints.
// It holds a pool of clients to interact with the backend service.
type ClaudeCodeAPIHandlers struct {
	*handlers.APIHandlers
}

// NewClaudeCodeAPIHandlers creates a new Claude API handlers instance.
// It takes an APIHandlers instance as input and returns a ClaudeCodeAPIHandlers.
func NewClaudeCodeAPIHandlers(apiHandlers *handlers.APIHandlers) *ClaudeCodeAPIHandlers {
	return &ClaudeCodeAPIHandlers{
		APIHandlers: apiHandlers,
	}
}

// ClaudeMessages handles Claude-compatible streaming chat completions.
// This function implements a sophisticated client rotation and quota management system
// to ensure high availability and optimal resource utilization across multiple backend clients.
func (h *ClaudeCodeAPIHandlers) ClaudeMessages(c *gin.Context) {
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

	// h.handleGeminiStreamingResponse(c, rawJSON)
	// h.handleCodexStreamingResponse(c, rawJSON)
	modelName := gjson.GetBytes(rawJSON, "model")
	provider := util.GetProviderName(modelName.String())

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.False {
		return
	}

	if provider == "gemini" {
		h.handleGeminiStreamingResponse(c, rawJSON)
	} else if provider == "gpt" {
		h.handleCodexStreamingResponse(c, rawJSON)
	} else if provider == "claude" {
		h.handleClaudeStreamingResponse(c, rawJSON)
	} else {
		h.handleGeminiStreamingResponse(c, rawJSON)
	}
}

// handleGeminiStreamingResponse streams Claude-compatible responses backed by Gemini.
// It sets up SSE, selects a backend client with rotation/quota logic,
// forwards chunks, and translates them to Claude CLI format.
func (h *ClaudeCodeAPIHandlers) handleGeminiStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Parse and prepare the Claude request, extracting model name, system instructions,
	// conversation contents, and available tools from the raw JSON
	modelName, systemInstruction, contents, tools := translatorClaudeCodeToGeminiCli.ConvertClaudeCodeRequestToCli(rawJSON)

	// Create a cancellable context for the backend client request
	// This allows proper cleanup and cancellation of ongoing requests
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	cliClient = client.NewGeminiClient(nil, nil, nil)
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		// This prevents deadlocks and ensures proper resource cleanup
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

	// Main client rotation loop with quota management
	// This loop implements a sophisticated load balancing and failover mechanism
outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			flusher.Flush()
			cliCancel()
			return
		}

		// Determine the authentication method being used by the selected client
		// This affects how responses are formatted and logged
		isGlAPIKey := false
		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use gemini generative language API Key: %s", glAPIKey)
			isGlAPIKey = true
		} else {
			log.Debugf("Request use gemini account: %s, project id: %s", cliClient.GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
		}
		// Initiate streaming communication with the backend client
		// This returns two channels: one for response chunks and one for errors

		respChan, errChan := cliClient.SendMessageStream(cliCtx, rawJSON, modelName, systemInstruction, contents, tools, true)

		// Track response state for proper Claude format conversion
		hasFirstResponse := false
		responseType := 0
		responseIndex := 0

		// Main streaming loop - handles multiple concurrent events using Go channels
		// This select statement manages four different types of events simultaneously
		for {
			select {
			// Case 1: Handle client disconnection
			// Detects when the HTTP client has disconnected and cleans up resources
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("GeminiClient disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request to prevent resource leaks
					return
				}

			// Case 2: Process incoming response chunks from the backend
			// This handles the actual streaming data from the AI model
			case chunk, okStream := <-respChan:
				if !okStream {
					// Stream has ended - send the final message_stop event
					// This follows the Claude API specification for stream termination
					_, _ = c.Writer.Write([]byte(`event: message_stop`))
					_, _ = c.Writer.Write([]byte("\n"))
					_, _ = c.Writer.Write([]byte(`data: {"type":"message_stop"}`))
					_, _ = c.Writer.Write([]byte("\n\n\n"))

					flusher.Flush()
					cliCancel()
					return
				}

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))
				// Convert the backend response to Claude-compatible format
				// This translation layer ensures API compatibility
				claudeFormat := translatorClaudeCodeToGeminiCli.ConvertCliResponseToClaudeCode(chunk, isGlAPIKey, hasFirstResponse, &responseType, &responseIndex)
				if claudeFormat != "" {
					_, _ = c.Writer.Write([]byte(claudeFormat))
					flusher.Flush() // Immediately send the chunk to the client
				}
				hasFirstResponse = true

			// Case 3: Handle errors from the backend
			// This manages various error conditions and implements retry logic
			case errInfo, okError := <-errChan:
				if okError {
					// Special handling for quota exceeded errors
					// If configured, attempt to switch to a different project/client
					if errInfo.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop // Restart the client selection process
					} else {
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
				if hasFirstResponse {
					// Send a ping event to maintain the connection
					// This is especially important for slow AI model responses
					// output := "event: ping\n"
					// output = output + `data: {"type": "ping"}`
					// output = output + "\n\n\n"
					// _, _ = c.Writer.Write([]byte(output))
					//
					// flusher.Flush()
				}
			}
		}
	}
}

// handleCodexStreamingResponse streams Claude-compatible responses backed by OpenAI.
// It converts the Claude request into Codex/OpenAI responses format, establishes SSE,
// and translates streaming chunks back into Claude CLI events.
func (h *ClaudeCodeAPIHandlers) handleCodexStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Parse and prepare the Claude request, extracting model name, system instructions,
	// conversation contents, and available tools from the raw JSON
	newRequestJSON := translatorClaudeCodeToCodex.ConvertClaudeCodeRequestToCodex(rawJSON)
	modelName := gjson.GetBytes(rawJSON, "model").String()

	newRequestJSON, _ = sjson.Set(newRequestJSON, "model", modelName)
	// log.Debugf(string(rawJSON))
	// log.Debugf(newRequestJSON)
	// return
	// Create a cancellable context for the backend client request
	// This allows proper cleanup and cancellation of ongoing requests
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		// This prevents deadlocks and ensures proper resource cleanup
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

	// Main client rotation loop with quota management
	// This loop implements a sophisticated load balancing and failover mechanism
outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			flusher.Flush()
			cliCancel()
			return
		}

		log.Debugf("Request use codex account: %s", cliClient.GetEmail())

		// Initiate streaming communication with the backend client
		// This returns two channels: one for response chunks and one for errors
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, []byte(newRequestJSON), "")

		// Track response state for proper Claude format conversion
		// hasFirstResponse := false
		hasToolCall := false

		// Main streaming loop - handles multiple concurrent events using Go channels
		// This select statement manages four different types of events simultaneously
		for {
			select {
			// Case 1: Handle client disconnection
			// Detects when the HTTP client has disconnected and cleans up resources
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("CodexClient disconnected: %v", c.Request.Context().Err())
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

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))

				// Convert the backend response to Claude-compatible format
				// This translation layer ensures API compatibility
				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					var claudeFormat string
					claudeFormat, hasToolCall = translatorClaudeCodeToCodex.ConvertCodexResponseToClaude(jsonData, hasToolCall)
					// log.Debugf("claudeFormat: %s", claudeFormat)
					if claudeFormat != "" {
						_, _ = c.Writer.Write([]byte(claudeFormat))
						_, _ = c.Writer.Write([]byte("\n"))
					}
					flusher.Flush() // Immediately send the chunk to the client
					// hasFirstResponse = true
				} else {
					// log.Debugf("chunk: %s", string(chunk))
				}
			// Case 3: Handle errors from the backend
			// This manages various error conditions and implements retry logic
			case errInfo, okError := <-errChan:
				if okError {
					// log.Debugf("Code: %d, Error: %v", errInfo.StatusCode, errInfo.Error)
					// Special handling for quota exceeded errors
					// If configured, attempt to switch to a different project/client
					if errInfo.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						log.Debugf("quota exceeded, switch client")
						continue outLoop // Restart the client selection process
					} else {
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
			case <-time.After(3000 * time.Millisecond):
				// if hasFirstResponse {
				// 	// Send a ping event to maintain the connection
				// 	// This is especially important for slow AI model responses
				// 	output := "event: ping\n"
				// 	output = output + `data: {"type": "ping"}`
				// 	output = output + "\n\n"
				// 	_, _ = c.Writer.Write([]byte(output))
				//
				// 	flusher.Flush()
				// }
			}
		}
	}
}

// handleClaudeStreamingResponse streams Claude-compatible responses backed by OpenAI.
// It converts the Claude request into OpenAI responses format, establishes SSE,
// and translates streaming chunks back into Claude Code events.
func (h *ClaudeCodeAPIHandlers) handleClaudeStreamingResponse(c *gin.Context, rawJSON []byte) {

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
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		// This prevents deadlocks and ensures proper resource cleanup
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

	// Main client rotation loop with quota management
	// This loop implements a sophisticated load balancing and failover mechanism
outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {

			if errorResponse.StatusCode == 429 {
				c.Header("Content-Type", "application/json")
				c.Header("Content-Length", fmt.Sprintf("%d", len(errorResponse.Error.Error())))
			}
			c.Status(errorResponse.StatusCode)

			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			flusher.Flush()
			cliCancel()

			return
		}

		if apiKey := cliClient.(*client.ClaudeClient).GetAPIKey(); apiKey != "" {
			log.Debugf("Request claude use API Key: %s", apiKey)
		} else {
			log.Debugf("Request claude use account: %s", cliClient.(*client.ClaudeClient).GetEmail())
		}

		// Initiate streaming communication with the backend client
		// This returns two channels: one for response chunks and one for errors
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, rawJSON, "")

		hasFirstResponse := false
		// Main streaming loop - handles multiple concurrent events using Go channels
		// This select statement manages four different types of events simultaneously
		for {
			select {
			// Case 1: Handle client disconnection
			// Detects when the HTTP client has disconnected and cleans up resources
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("ClaudeClient disconnected: %v", c.Request.Context().Err())
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
				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))

				if !hasFirstResponse {
					// Set up Server-Sent Events (SSE) headers for streaming response
					// These headers are essential for maintaining a persistent connection
					// and enabling real-time streaming of chat completions
					c.Header("Content-Type", "text/event-stream")
					c.Header("Cache-Control", "no-cache")
					c.Header("Connection", "keep-alive")
					c.Header("Access-Control-Allow-Origin", "*")
					hasFirstResponse = true
				}

				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()

			// Case 3: Handle errors from the backend
			// This manages various error conditions and implements retry logic
			case errInfo, okError := <-errChan:
				if okError {
					// log.Debugf("Code: %d, Error: %v", errInfo.StatusCode, errInfo.Error)
					// Special handling for quota exceeded errors
					// If configured, attempt to switch to a different project/client
					// if errInfo.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
					if errInfo.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						log.Debugf("quota exceeded, switch client")
						continue outLoop // Restart the client selection process
					} else {
						// Forward other errors directly to the client
						if errInfo.Addon != nil {
							for key, val := range errInfo.Addon {
								c.Header(key, val[0])
							}
						}

						c.Status(errInfo.StatusCode)

						_, _ = fmt.Fprint(c.Writer, errInfo.Error.Error())
						flusher.Flush()
						cliCancel(errInfo.Error)
					}
					return
				}

			// Case 4: Send periodic keep-alive signals
			// Prevents connection timeouts during long-running requests
			case <-time.After(3000 * time.Millisecond):
			}
		}
	}
}
