package api

import (
	"bytes"
	"context"
	"fmt"
	"github.com/luispater/CLIProxyAPI/internal/api/translator"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"net/http"
	"net/url"
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
				"id":                    "gemini-2.5-pro-preview-05-06",
				"object":                "model",
				"version":               "2.5-preview-05-06",
				"name":                  "Gemini 2.5 Pro Preview 05-06",
				"description":           "Preview release (May 6th, 2025) of Gemini 2.5 Pro",
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
				"id":                    "gemini-2.5-pro-preview-06-05",
				"object":                "model",
				"version":               "2.5-preview-06-05",
				"name":                  "Gemini 2.5 Pro Preview 06-05",
				"description":           "Preview release (June 5th, 2025) of Gemini 2.5 Pro",
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
				"id":                    "gemini-2.5-flash-preview-04-17",
				"object":                "model",
				"version":               "2.5-preview-04-17",
				"name":                  "Gemini 2.5 Flash Preview 04-17",
				"description":           "Preview release (April 17th, 2025) of Gemini 2.5 Flash",
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
				"id":                    "gemini-2.5-flash-preview-05-20",
				"object":                "model",
				"version":               "2.5-preview-05-20",
				"name":                  "Gemini 2.5 Flash Preview 05-20",
				"description":           "Preview release (April 17th, 2025) of Gemini 2.5 Flash",
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
		// Lock the mutex to update the last used client index
		mutex.Lock()
		startIndex := lastUsedClientIndex
		currentIndex := (startIndex + 1) % len(h.cliClients)
		lastUsedClientIndex = currentIndex
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
			c.Status(429)
			_, _ = c.Writer.Write([]byte(fmt.Sprintf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName)))
			cliCancel()
			return
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
		// Lock the mutex to update the last used client index
		mutex.Lock()
		startIndex := lastUsedClientIndex
		currentIndex := (startIndex + 1) % len(h.cliClients)
		lastUsedClientIndex = currentIndex
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
			c.Status(429)
			_, _ = fmt.Fprint(c.Writer, fmt.Sprintf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName))
			flusher.Flush()
			cliCancel()
			return
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

func (h *APIHandlers) Internal(c *gin.Context) {
	rawJson, _ := c.GetRawData()
	requestRawURI := c.Request.URL.Path
	if requestRawURI == "/v1internal:generateContent" {
		h.internalGenerateContent(c, rawJson)
	} else if requestRawURI == "/v1internal:streamGenerateContent" {
		h.internalStreamGenerateContent(c, rawJson)
	} else {
		reqBody := bytes.NewBuffer(rawJson)
		req, err := http.NewRequest("POST", fmt.Sprintf("https://cloudcode-pa.googleapis.com%s", c.Request.URL.RequestURI()), reqBody)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", err),
					Type:    "invalid_request_error",
				},
			})
			return
		}
		for key, value := range c.Request.Header {
			req.Header[key] = value
		}

		var transport *http.Transport
		proxyURL, errParse := url.Parse(h.cfg.ProxyUrl)
		if errParse == nil {
			if proxyURL.Scheme == "socks5" {
				username := proxyURL.User.Username()
				password, _ := proxyURL.User.Password()
				proxyAuth := &proxy.Auth{User: username, Password: password}
				dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
				if errSOCKS5 != nil {
					log.Fatalf("create SOCKS5 dialer failed: %v", errSOCKS5)
				}
				transport = &http.Transport{
					DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						return dialer.Dial(network, addr)
					},
				}
			} else if proxyURL.Scheme == "http" || proxyURL.Scheme == "https" {
				transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
			}
		}
		httpClient := &http.Client{}
		if transport != nil {
			httpClient.Transport = transport
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", err),
					Type:    "invalid_request_error",
				},
			})
			return
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer func() {
				if err = resp.Body.Close(); err != nil {
					log.Printf("warn: failed to close response body: %v", err)
				}
			}()
			bodyBytes, _ := io.ReadAll(resp.Body)

			c.JSON(http.StatusBadRequest, ErrorResponse{
				Error: ErrorDetail{
					Message: string(bodyBytes),
					Type:    "invalid_request_error",
				},
			})
			return
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		for key, value := range resp.Header {
			c.Header(key, value[0])
		}
		output, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Errorf("Failed to read response body: %v", err)
			return
		}
		_, _ = c.Writer.Write(output)
	}
}

func (h *APIHandlers) internalStreamGenerateContent(c *gin.Context, rawJson []byte) {
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

	modelResult := gjson.GetBytes(rawJson, "model")
	modelName := modelResult.String()

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
		// Lock the mutex to update the last used client index
		mutex.Lock()
		startIndex := lastUsedClientIndex
		currentIndex := (startIndex + 1) % len(h.cliClients)
		lastUsedClientIndex = currentIndex
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
			c.Status(429)
			_, _ = fmt.Fprint(c.Writer, fmt.Sprintf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName))
			flusher.Flush()
			cliCancel()
			return
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

		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}
		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, rawJson)
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
					cliCancel()
					return
				} else {
					hasFirstResponse = true
					if cliClient.GetGenerativeLanguageAPIKey() != "" {
						chunk, _ = sjson.SetRawBytes(chunk, "response", chunk)
					}
					_, _ = c.Writer.Write([]byte("data: "))
					_, _ = c.Writer.Write(chunk)
					_, _ = c.Writer.Write([]byte("\n\n"))
					flusher.Flush()
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
					_, _ = c.Writer.Write([]byte("\n"))
					flusher.Flush()
				}
			}
		}
	}
}

func (h *APIHandlers) internalGenerateContent(c *gin.Context, rawJson []byte) {
	c.Header("Content-Type", "application/json")

	modelResult := gjson.GetBytes(rawJson, "model")
	modelName := modelResult.String()
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
		}
	}()

	for {
		// Lock the mutex to update the last used client index
		mutex.Lock()
		startIndex := lastUsedClientIndex
		currentIndex := (startIndex + 1) % len(h.cliClients)
		lastUsedClientIndex = currentIndex
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
			c.Status(429)
			_, _ = c.Writer.Write([]byte(fmt.Sprintf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName)))
			cliCancel()
			return
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

		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}

		resp, err := cliClient.SendRawMessage(cliCtx, rawJson)
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
			_, _ = c.Writer.Write(resp)
			cliCancel()
			break
		}
	}
}
