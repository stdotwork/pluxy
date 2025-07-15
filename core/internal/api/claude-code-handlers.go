package api

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/translator"
	"github.com/luispater/CLIProxyAPI/internal/client"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strings"
	"time"
)

// ClaudeMessages handles Claude-compatible streaming chat completions.
// This function implements a sophisticated client rotation and quota management system
// to ensure high availability and optimal resource utilization across multiple backend clients.
func (h *APIHandlers) ClaudeMessages(c *gin.Context) {
	// Extract raw JSON data from the incoming request
	rawJson, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

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
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	// Parse and prepare the Claude request, extracting model name, system instructions,
	// conversation contents, and available tools from the raw JSON
	modelName, systemInstruction, contents, tools := translator.PrepareClaudeRequest(rawJson)

	// Map Claude model names to corresponding Gemini models
	// This allows the proxy to handle Claude API calls using Gemini backend
	if modelName == "claude-sonnet-4-20250514" {
		modelName = "gemini-2.5-pro"
	} else if modelName == "claude-3-5-haiku-20241022" {
		modelName = "gemini-2.5-flash"
	}

	// Create a cancellable context for the backend client request
	// This allows proper cleanup and cancellation of ongoing requests
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		// This prevents deadlocks and ensures proper resource cleanup
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
		}
	}()

	// Main client rotation loop with quota management
	// This loop implements a sophisticated load balancing and failover mechanism
outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.getClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			flusher.Flush()
			cliCancel()
			return
		}

		// Determine the authentication method being used by the selected client
		// This affects how responses are formatted and logged
		isGlAPIKey := false
		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
			isGlAPIKey = true
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}
		// Initiate streaming communication with the backend client
		// This returns two channels: one for response chunks and one for errors

		includeThoughts := false
		if userAgent, hasKey := c.Request.Header["User-Agent"]; hasKey {
			includeThoughts = !strings.Contains(userAgent[0], "claude-cli")
		}

		respChan, errChan := cliClient.SendMessageStream(cliCtx, rawJson, modelName, systemInstruction, contents, tools, includeThoughts)

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
					log.Debugf("Client disconnected: %v", c.Request.Context().Err())
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
				} else {
					// Convert the backend response to Claude-compatible format
					// This translation layer ensures API compatibility
					claudeFormat := translator.ConvertCliToClaude(chunk, isGlAPIKey, hasFirstResponse, &responseType, &responseIndex)
					if claudeFormat != "" {
						_, _ = c.Writer.Write([]byte(claudeFormat))
						flusher.Flush() // Immediately send the chunk to the client
					}
					hasFirstResponse = true
				}

			// Case 3: Handle errors from the backend
			// This manages various error conditions and implements retry logic
			case errInfo, okError := <-errChan:
				if okError {
					// Special handling for quota exceeded errors
					// If configured, attempt to switch to a different project/client
					if errInfo.StatusCode == 429 && h.cfg.QuotaExceeded.SwitchProject {
						continue outLoop // Restart the client selection process
					} else {
						// Forward other errors directly to the client
						c.Status(errInfo.StatusCode)
						_, _ = fmt.Fprint(c.Writer, errInfo.Error.Error())
						flusher.Flush()
						cliCancel()
					}
					return
				}

			// Case 4: Send periodic keep-alive signals
			// Prevents connection timeouts during long-running requests
			case <-time.After(500 * time.Millisecond):
				if hasFirstResponse {
					// Send a ping event to maintain the connection
					// This is especially important for slow AI model responses
					output := "event: ping\n"
					output = output + `data: {"type": "ping"}`
					output = output + "\n\n\n"
					_, _ = c.Writer.Write([]byte(output))

					flusher.Flush()
				}
			}
		}
	}

}
