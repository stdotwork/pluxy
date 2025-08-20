// Package openai provides HTTP handlers for OpenAI API endpoints.
// This package implements the OpenAI-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAI API requests to the appropriate backend format and
// convert responses back to OpenAI-compatible format.
package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	"github.com/luispater/CLIProxyAPI/internal/client"
	translatorOpenAIToClaude "github.com/luispater/CLIProxyAPI/internal/translator/claude/openai"
	translatorOpenAIToCodex "github.com/luispater/CLIProxyAPI/internal/translator/codex/openai"
	translatorOpenAIToGeminiCli "github.com/luispater/CLIProxyAPI/internal/translator/gemini-cli/openai"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

// OpenAIAPIHandlers contains the handlers for OpenAI API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIAPIHandlers struct {
	*handlers.APIHandlers
}

// NewOpenAIAPIHandlers creates a new OpenAI API handlers instance.
// It takes an APIHandlers instance as input and returns an OpenAIAPIHandlers.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIAPIHandlers: A new OpenAI API handlers instance
func NewOpenAIAPIHandlers(apiHandlers *handlers.APIHandlers) *OpenAIAPIHandlers {
	return &OpenAIAPIHandlers{
		APIHandlers: apiHandlers,
	}
}

// Models handles the /v1/models endpoint.
// It returns a hardcoded list of available AI models with their capabilities
// and specifications in OpenAI-compatible format.
func (h *OpenAIAPIHandlers) Models(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"data": []map[string]any{
			{
				"id":                    "gemini-2.5-pro",
				"object":                "model",
				"version":               "2.5",
				"name":                  "Gemini 2.5 Pro",
				"description":           "Stable release (June 17th, 2025) of Gemini 2.5 Pro",
				"context_length":        1_048_576,
				"max_completion_tokens": 65_536,
				"supported_parameters": []string{
					"tools",
					"temperature",
					"top_p",
					"top_k",
				},
				"temperature":    1,
				"topP":           0.95,
				"topK":           64,
				"maxTemperature": 2,
				"thinking":       true,
			},
			{
				"id":                    "gemini-2.5-flash",
				"object":                "model",
				"version":               "001",
				"name":                  "Gemini 2.5 Flash",
				"description":           "Stable version of Gemini 2.5 Flash, our mid-size multimodal model that supports up to 1 million tokens, released in June of 2025.",
				"context_length":        1_048_576,
				"max_completion_tokens": 65_536,
				"supported_parameters": []string{
					"tools",
					"temperature",
					"top_p",
					"top_k",
				},
				"temperature":    1,
				"topP":           0.95,
				"topK":           64,
				"maxTemperature": 2,
				"thinking":       true,
			},
			{
				"id":                    "gpt-5",
				"object":                "model",
				"version":               "gpt-5-2025-08-07",
				"name":                  "GPT 5",
				"description":           "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
				"context_length":        400_000,
				"max_completion_tokens": 128_000,
				"supported_parameters": []string{
					"tools",
				},
				"temperature":    1,
				"topP":           0.95,
				"topK":           64,
				"maxTemperature": 2,
				"thinking":       true,
			},
			{
				"id":                    "claude-opus-4-1-20250805",
				"object":                "model",
				"version":               "claude-opus-4-1-20250805",
				"name":                  "Claude Opus 4.1",
				"description":           "Anthropic's most capable model.",
				"context_length":        200_000,
				"max_completion_tokens": 32_000,
				"supported_parameters": []string{
					"tools",
				},
				"temperature":    1,
				"topP":           0.95,
				"topK":           64,
				"maxTemperature": 2,
				"thinking":       true,
			},
		},
	})
}

// ChatCompletions handles the /v1/chat/completions endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandlers) ChatCompletions(c *gin.Context) {
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
	modelName := gjson.GetBytes(rawJSON, "model")
	provider := util.GetProviderName(modelName.String())
	if provider == "gemini" {
		if streamResult.Type == gjson.True {
			h.handleGeminiStreamingResponse(c, rawJSON)
		} else {
			h.handleGeminiNonStreamingResponse(c, rawJSON)
		}
	} else if provider == "gpt" {
		if streamResult.Type == gjson.True {
			h.handleCodexStreamingResponse(c, rawJSON)
		} else {
			h.handleCodexNonStreamingResponse(c, rawJSON)
		}
	} else if provider == "claude" {
		if streamResult.Type == gjson.True {
			h.handleClaudeStreamingResponse(c, rawJSON)
		} else {
			h.handleClaudeNonStreamingResponse(c, rawJSON)
		}
	} else if provider == "qwen" {
		// qwen3-coder-plus / qwen3-coder-flash
		if streamResult.Type == gjson.True {
			h.handleQwenStreamingResponse(c, rawJSON)
		} else {
			h.handleQwenNonStreamingResponse(c, rawJSON)
		}
	}
}

// handleGeminiNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAI format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleGeminiNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName, systemInstruction, contents, tools := translatorOpenAIToGeminiCli.ConvertOpenAIChatRequestToCli(rawJSON)
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		isGlAPIKey := false
		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
			isGlAPIKey = true
		} else {
			log.Debugf("Request cli use account: %s, project id: %s", cliClient.(*client.GeminiClient).GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
		}

		resp, err := cliClient.SendMessage(cliCtx, rawJSON, modelName, systemInstruction, contents, tools)
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
			openAIFormat := translatorOpenAIToGeminiCli.ConvertCliResponseToOpenAIChatNonStream(resp, time.Now().Unix(), isGlAPIKey)
			if openAIFormat != "" {
				_, _ = c.Writer.Write([]byte(openAIFormat))
			}
			cliCancel(resp)
			break
		}
	}
}

// handleGeminiStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleGeminiStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Prepare the request for the backend client.
	modelName, systemInstruction, contents, tools := translatorOpenAIToGeminiCli.ConvertOpenAIChatRequestToCli(rawJSON)
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

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

		isGlAPIKey := false
		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
			isGlAPIKey = true
		} else {
			log.Debugf("Request cli use account: %s, project id: %s", cliClient.GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
		}
		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendMessageStream(cliCtx, rawJSON, modelName, systemInstruction, contents, tools)

		hasFirstResponse := false
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("GeminiClient disconnected: %v", c.Request.Context().Err())
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

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))

				// Convert the chunk to OpenAI format and send it to the client.
				hasFirstResponse = true
				openAIFormat := translatorOpenAIToGeminiCli.ConvertCliResponseToOpenAIChat(chunk, time.Now().Unix(), isGlAPIKey)
				if openAIFormat != "" {
					_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", openAIFormat)
					flusher.Flush()
				}
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
						c.Status(err.StatusCode)
						_, _ = fmt.Fprint(c.Writer, err.Error.Error())
						flusher.Flush()
						cliCancel(err.Error)
					}
					return
				}
			// Send a keep-alive signal to the client.
			case <-time.After(500 * time.Millisecond):
				if hasFirstResponse {
					_, _ = c.Writer.Write([]byte(": CLI-PROXY-API PROCESSING\n\n"))
					flusher.Flush()
				}
			}
		}
	}
}

// handleCodexNonStreamingResponse handles non-streaming chat completion responses
// for OpenAI models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAI format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleCodexNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	newRequestJSON := translatorOpenAIToCodex.ConvertOpenAIChatRequestToCodex(rawJSON)
	modelName := gjson.GetBytes(rawJSON, "model")

	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName.String())
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = c.Writer.Write([]byte(errorResponse.Error.Error()))
			cliCancel()
			return
		}

		log.Debugf("Request codex use account: %s", cliClient.GetEmail())

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, []byte(newRequestJSON), "")
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("CodexClient disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					cliCancel()
					return
				}

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))

				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					data := gjson.ParseBytes(jsonData)
					typeResult := data.Get("type")
					if typeResult.String() == "response.completed" {
						responseResult := data.Get("response")
						openaiStr := translatorOpenAIToCodex.ConvertCodexResponseToOpenAIChatNonStream(responseResult.Raw, time.Now().Unix())
						_, _ = c.Writer.Write([]byte(openaiStr))
					}
				}
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
						c.Status(err.StatusCode)
						_, _ = c.Writer.Write([]byte(err.Error.Error()))
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

// handleCodexStreamingResponse handles streaming responses for OpenAI models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleCodexStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Prepare the request for the backend client.
	newRequestJSON := translatorOpenAIToCodex.ConvertOpenAIChatRequestToCodex(rawJSON)
	// log.Debugf("Request: %s", newRequestJSON)

	modelName := gjson.GetBytes(rawJSON, "model")

	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName.String())
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			flusher.Flush()
			cliCancel()
			return
		}

		log.Debugf("Request codex use account: %s", cliClient.GetEmail())

		// Send the message and receive response chunks and errors via channels.
		var params *translatorOpenAIToCodex.ConvertCliToOpenAIParams
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, []byte(newRequestJSON), "")
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("CodexClient disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					_, _ = c.Writer.Write([]byte("[done]\n\n"))
					flusher.Flush()
					cliCancel()
					return
				}

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))

				// log.Debugf("Response: %s\n", string(chunk))
				// Convert the chunk to OpenAI format and send it to the client.
				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					data := gjson.ParseBytes(jsonData)
					typeResult := data.Get("type")
					if typeResult.String() != "" {
						var openaiStr string
						params, openaiStr = translatorOpenAIToCodex.ConvertCodexResponseToOpenAIChat(jsonData, params)
						if openaiStr != "" {
							_, _ = c.Writer.Write([]byte("data: "))
							_, _ = c.Writer.Write([]byte(openaiStr))
							_, _ = c.Writer.Write([]byte("\n\n"))
						}
					}
					// log.Debugf(string(jsonData))
				}
				flusher.Flush()
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
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

// handleClaudeNonStreamingResponse handles non-streaming chat completion responses
// for anthropic models. It uses the streaming interface internally but aggregates
// all responses before sending back a complete non-streaming response in OpenAI format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleClaudeNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	// Force streaming in the request to use the streaming interface
	newRequestJSON := translatorOpenAIToClaude.ConvertOpenAIRequestToAnthropic(rawJSON)
	// Ensure stream is set to true for the backend request
	newRequestJSON, _ = sjson.Set(newRequestJSON, "stream", true)

	modelName := gjson.GetBytes(rawJSON, "model")
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName.String())
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		if apiKey := cliClient.(*client.ClaudeClient).GetAPIKey(); apiKey != "" {
			log.Debugf("Request claude use API Key: %s", apiKey)
		} else {
			log.Debugf("Request claude use account: %s", cliClient.(*client.ClaudeClient).GetEmail())
		}

		// Use streaming interface but collect all responses
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, []byte(newRequestJSON), "")

		// Collect all streaming chunks to build the final response
		var allChunks [][]byte

		for {
			select {
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("Client disconnected: %v", c.Request.Context().Err())
					cliCancel()
					return
				}
			case chunk, okStream := <-respChan:
				if !okStream {
					// All chunks received, now build the final non-streaming response
					if len(allChunks) > 0 {
						// Use the last chunk which should contain the complete message
						finalResponseStr := translatorOpenAIToClaude.ConvertAnthropicStreamingResponseToOpenAINonStream(allChunks)
						finalResponse := []byte(finalResponseStr)
						_, _ = c.Writer.Write(finalResponse)
					}
					cliCancel()
					return
				}

				// Store chunk for building final response
				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					allChunks = append(allChunks, jsonData)
				}

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))

			case err, okError := <-errChan:
				if okError {
					if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
						c.Status(err.StatusCode)
						_, _ = fmt.Fprint(c.Writer, err.Error.Error())
						cliCancel(err.Error)
					}
					return
				}
			case <-time.After(30 * time.Second):
			}
		}
	}
}

// handleClaudeStreamingResponse handles streaming responses for anthropic models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleClaudeStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Prepare the request for the backend client.
	newRequestJSON := translatorOpenAIToClaude.ConvertOpenAIRequestToAnthropic(rawJSON)
	modelName := gjson.GetBytes(rawJSON, "model")
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

outLoop:
	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName.String())
		if errorResponse != nil {
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

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, []byte(newRequestJSON), "")
		params := &translatorOpenAIToClaude.ConvertAnthropicResponseToOpenAIParams{
			CreatedAt:    0,
			ResponseID:   "",
			FinishReason: "",
		}

		hasFirstResponse := false
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("GeminiClient disconnected: %v", c.Request.Context().Err())
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

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n\n"))

				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					// Convert the chunk to OpenAI format and send it to the client.
					hasFirstResponse = true
					openAIFormats := translatorOpenAIToClaude.ConvertAnthropicResponseToOpenAI(jsonData, params)
					for i := 0; i < len(openAIFormats); i++ {
						_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", openAIFormats[i])
						flusher.Flush()
					}
				}
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
						c.Status(err.StatusCode)
						_, _ = fmt.Fprint(c.Writer, err.Error.Error())
						flusher.Flush()
						cliCancel(err.Error)
					}
					return
				}
			// Send a keep-alive signal to the client.
			case <-time.After(500 * time.Millisecond):
				if hasFirstResponse {
					_, _ = c.Writer.Write([]byte(": CLI-PROXY-API PROCESSING\n\n"))
					flusher.Flush()
				}
			}
		}
	}
}

// handleQwenNonStreamingResponse handles non-streaming chat completion responses
// for Qwen models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAI format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleQwenNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()
	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		log.Debugf("Request qwen use account: %s", cliClient.(*client.QwenClient).GetEmail())

		resp, err := cliClient.SendRawMessage(cliCtx, rawJSON, modelName)
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

// handleQwenStreamingResponse handles streaming responses for Qwen models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandlers) handleQwenStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Prepare the request for the backend client.
	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()

	cliCtx, cliCancel := h.GetContextWithCancel(c, context.Background())

	var cliClient client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

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

		log.Debugf("Request qwen use account: %s", cliClient.(*client.QwenClient).GetEmail())

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, rawJSON, modelName)

		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("GeminiClient disconnected: %v", c.Request.Context().Err())
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

				h.AddAPIResponseData(c, chunk)
				h.AddAPIResponseData(c, []byte("\n"))

				// Convert the chunk to OpenAI format and send it to the client.
				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n"))

				flusher.Flush()
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
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
