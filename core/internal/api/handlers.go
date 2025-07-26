package api

import (
	"context"
	"fmt"
	"github.com/luispater/CLIProxyAPI/internal/api/translator"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	mutex               = &sync.Mutex{}
	lastUsedClientIndex = 0
)

// APIHandlers contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service.
type APIHandlers struct {
	cliClients []*client.Client
	cfg        *config.Config
}

// NewAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and a debug flag as input.
func NewAPIHandlers(cliClients []*client.Client, cfg *config.Config) *APIHandlers {
	return &APIHandlers{
		cliClients: cliClients,
		cfg:        cfg,
	}
}

// Models handles the /v1/models endpoint.
// It returns a hardcoded list of available AI models.
func (h *APIHandlers) Models(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"data": []map[string]any{
			{
				"id":                    "gemini-2.5-pro",
				"object":                "model",
				"version":               "2.5",
				"name":                  "Gemini 2.5 Pro",
				"description":           "Stable release (June 17th, 2025) of Gemini 2.5 Pro",
				"context_length":        1048576,
				"max_completion_tokens": 65536,
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
				"context_length":        1048576,
				"max_completion_tokens": 65536,
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
		},
	})
}

func (h *APIHandlers) getClient(modelName string, isGenerateContent ...bool) (*client.Client, *client.ErrorMessage) {
	if len(h.cliClients) == 0 {
		return nil, &client.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("no clients available")}
	}

	var cliClient *client.Client

	// Lock the mutex to update the last used client index
	mutex.Lock()
	startIndex := lastUsedClientIndex
	if (len(isGenerateContent) > 0 && isGenerateContent[0]) || len(isGenerateContent) == 0 {
		currentIndex := (startIndex + 1) % len(h.cliClients)
		lastUsedClientIndex = currentIndex
	}
	mutex.Unlock()

	// Reorder the client to start from the last used index
	reorderedClients := make([]*client.Client, 0)
	for i := 0; i < len(h.cliClients); i++ {
		cliClient = h.cliClients[(startIndex+1+i)%len(h.cliClients)]
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
		cliClient = h.cliClients[0]
		cliClient.RequestMutex.Lock()
	}

	return cliClient, nil
}

// ChatCompletions handles the /v1/chat/completions endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler.
func (h *APIHandlers) ChatCompletions(c *gin.Context) {
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

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJson, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJson)
	} else {
		h.handleNonStreamingResponse(c, rawJson)
	}
}

// handleNonStreamingResponse handles non-streaming chat completion responses.
// It selects a client from the pool, sends the request, and aggregates the response
// before sending it back to the client.
func (h *APIHandlers) handleNonStreamingResponse(c *gin.Context, rawJson []byte) {
	c.Header("Content-Type", "application/json")

	modelName, systemInstruction, contents, tools := translator.PrepareRequest(rawJson)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
		}
	}()

	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.getClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			cliCancel()
			return
		}

		isGlAPIKey := false
		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
			isGlAPIKey = true
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}

		resp, err := cliClient.SendMessage(cliCtx, rawJson, modelName, systemInstruction, contents, tools)
		if err != nil {
			if err.StatusCode == 429 && h.cfg.QuotaExceeded.SwitchProject {
				continue
			} else {
				c.Status(err.StatusCode)
				_, _ = c.Writer.Write([]byte(err.Error.Error()))
				cliCancel()
			}
			break
		} else {
			openAIFormat := translator.ConvertCliToOpenAINonStream(resp, time.Now().Unix(), isGlAPIKey)
			if openAIFormat != "" {
				_, _ = c.Writer.Write([]byte(openAIFormat))
			}
			cliCancel()
			break
		}
	}
}

// handleStreamingResponse handles streaming responses
func (h *APIHandlers) handleStreamingResponse(c *gin.Context, rawJson []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	// Get the http.Flusher interface to manually flush the response.
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

	// Prepare the request for the backend client.
	modelName, systemInstruction, contents, tools := translator.PrepareRequest(rawJson)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		// Ensure the client's mutex is unlocked on function exit.
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
		}
	}()

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

		isGlAPIKey := false
		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
			isGlAPIKey = true
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}
		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendMessageStream(cliCtx, rawJson, modelName, systemInstruction, contents, tools)
		hasFirstResponse := false
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("Client disconnected: %v", c.Request.Context().Err())
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
				} else {
					// Convert the chunk to OpenAI format and send it to the client.
					hasFirstResponse = true
					openAIFormat := translator.ConvertCliToOpenAI(chunk, time.Now().Unix(), isGlAPIKey)
					if openAIFormat != "" {
						_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", openAIFormat)
						flusher.Flush()
					}
				}
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					if err.StatusCode == 429 && h.cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
						c.Status(err.StatusCode)
						_, _ = fmt.Fprint(c.Writer, err.Error.Error())
						flusher.Flush()
						cliCancel()
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
