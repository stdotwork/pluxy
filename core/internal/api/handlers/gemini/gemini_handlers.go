// Package gemini provides HTTP handlers for Gemini API endpoints.
// This package implements handlers for managing Gemini model operations including
// model listing, content generation, streaming content generation, and token counting.
// It serves as a proxy layer between clients and the Gemini backend service,
// handling request translation, client management, and response processing.
package gemini

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	"github.com/luispater/CLIProxyAPI/internal/client"
	translatorGeminiToCodex "github.com/luispater/CLIProxyAPI/internal/translator/codex/gemini"
	translatorGeminiToGeminiCli "github.com/luispater/CLIProxyAPI/internal/translator/gemini-cli/gemini/cli"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// GeminiAPIHandlers contains the handlers for Gemini API endpoints.
// It holds a pool of clients to interact with the backend service.
type GeminiAPIHandlers struct {
	*handlers.APIHandlers
}

// NewGeminiAPIHandlers creates a new Gemini API handlers instance.
// It takes an APIHandlers instance as input and returns a GeminiAPIHandlers.
func NewGeminiAPIHandlers(apiHandlers *handlers.APIHandlers) *GeminiAPIHandlers {
	return &GeminiAPIHandlers{
		APIHandlers: apiHandlers,
	}
}

// GeminiModels handles the Gemini models listing endpoint.
// It returns a JSON response containing available Gemini models and their specifications.
func (h *GeminiAPIHandlers) GeminiModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"data": []map[string]any{
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
		},
	})
}

// GeminiGetHandler handles GET requests for specific Gemini model information.
// It returns detailed information about a specific Gemini model based on the action parameter.
func (h *GeminiAPIHandlers) GeminiGetHandler(c *gin.Context) {
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
		})
	case "gemini-2.5-flash":
		c.JSON(http.StatusOK, gin.H{
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
		})
	case "gpt-5":
		c.JSON(http.StatusOK, gin.H{
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
func (h *GeminiAPIHandlers) GeminiHandler(c *gin.Context) {
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

	modelName := action[0]
	method := action[1]
	rawJSON, _ := c.GetRawData()
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", []byte(modelName))

	provider := util.GetProviderName(modelName)
	if provider == "gemini" || provider == "unknow" {
		switch method {
		case "generateContent":
			h.handleGeminiGenerateContent(c, rawJSON)
		case "streamGenerateContent":
			h.handleGeminiStreamGenerateContent(c, rawJSON)
		case "countTokens":
			h.handleGeminiCountTokens(c, rawJSON)
		}
	} else if provider == "gpt" {
		switch method {
		case "generateContent":
			h.handleCodexGenerateContent(c, rawJSON)
		case "streamGenerateContent":
			h.handleCodexStreamGenerateContent(c, rawJSON)
		}

	}
}

func (h *GeminiAPIHandlers) handleGeminiStreamGenerateContent(c *gin.Context, rawJSON []byte) {
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

	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()

	backgroundCtx, cliCancel := context.WithCancel(context.Background())
	cliCtx := context.WithValue(backgroundCtx, "gin", c)

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
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			flusher.Flush()
			cliCancel()
			return
		}

		template := ""
		parsed := gjson.Parse(string(rawJSON))
		contents := parsed.Get("request.contents")
		if contents.Exists() {
			template = string(rawJSON)
		} else {
			template = `{"project":"","request":{},"model":""}`
			template, _ = sjson.SetRaw(template, "request", string(rawJSON))
			template, _ = sjson.Set(template, "model", gjson.Get(template, "request.model").String())
			template, _ = sjson.Delete(template, "request.model")
		}

		template, errFixCLIToolResponse := translatorGeminiToGeminiCli.FixCLIToolResponse(template)
		if errFixCLIToolResponse != nil {
			c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: errFixCLIToolResponse.Error(),
					Type:    "server_error",
				},
			})
			cliCancel()
			return
		}

		systemInstructionResult := gjson.Get(template, "request.system_instruction")
		if systemInstructionResult.Exists() {
			template, _ = sjson.SetRaw(template, "request.systemInstruction", systemInstructionResult.Raw)
			template, _ = sjson.Delete(template, "request.system_instruction")
		}
		rawJSON = []byte(template)

		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request cli use account: %s, project id: %s", cliClient.(*client.GeminiClient).GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
		}

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, rawJSON, alt)
		apiResponseData := make([]byte, 0)
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("GeminiClient disconnected: %v", c.Request.Context().Err())
					c.Set("API_RESPONSE", apiResponseData)
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					c.Set("API_RESPONSE", apiResponseData)
					cliCancel()
					return
				}
				apiResponseData = append(apiResponseData, chunk...)

				if cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey() == "" {
					if alt == "" {
						responseResult := gjson.GetBytes(chunk, "response")
						if responseResult.Exists() {
							chunk = []byte(responseResult.Raw)
						}
					} else {
						chunkTemplate := "[]"
						responseResult := gjson.ParseBytes(chunk)
						if responseResult.IsArray() {
							responseResultItems := responseResult.Array()
							for i := 0; i < len(responseResultItems); i++ {
								responseResultItem := responseResultItems[i]
								if responseResultItem.Get("response").Exists() {
									chunkTemplate, _ = sjson.SetRaw(chunkTemplate, "-1", responseResultItem.Get("response").Raw)
								}
							}
						}
						chunk = []byte(chunkTemplate)
					}
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
					if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						log.Debugf("quota exceeded, switch client")
						continue outLoop
					} else {
						log.Debugf("error code :%d, error: %v", err.StatusCode, err.Error.Error())
						c.Status(err.StatusCode)
						_, _ = fmt.Fprint(c.Writer, err.Error.Error())
						flusher.Flush()
						c.Set("API_RESPONSE", []byte(err.Error.Error()))
						cliCancel()
					}
					return
				}
			// Send a keep-alive signal to the client.
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func (h *GeminiAPIHandlers) handleGeminiCountTokens(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)
	// orgrawJSON := rawJSON
	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()
	backgroundCtx, cliCancel := context.WithCancel(context.Background())
	cliCtx := context.WithValue(backgroundCtx, "gin", c)

	var cliClient client.Client
	defer func() {
		if cliClient != nil {
			cliClient.GetRequestMutex().Unlock()
		}
	}()

	for {
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.GetClient(modelName, false)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			cliCancel()
			return
		}

		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request cli use account: %s, project id: %s", cliClient.(*client.GeminiClient).GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())

			template := `{"request":{}}`
			if gjson.GetBytes(rawJSON, "generateContentRequest").Exists() {
				template, _ = sjson.SetRaw(template, "request", gjson.GetBytes(rawJSON, "generateContentRequest").Raw)
				template, _ = sjson.Delete(template, "generateContentRequest")
			} else if gjson.GetBytes(rawJSON, "contents").Exists() {
				template, _ = sjson.SetRaw(template, "request.contents", gjson.GetBytes(rawJSON, "contents").Raw)
				template, _ = sjson.Delete(template, "contents")
			}
			rawJSON = []byte(template)
		}

		resp, err := cliClient.SendRawTokenCount(cliCtx, rawJSON, alt)
		if err != nil {
			if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
				continue
			} else {
				c.Status(err.StatusCode)
				_, _ = c.Writer.Write([]byte(err.Error.Error()))
				c.Set("API_RESPONSE", []byte(err.Error.Error()))
				cliCancel()
				// log.Debugf(err.Error.Error())
				// log.Debugf(string(rawJSON))
				// log.Debugf(string(orgrawJSON))
			}
			break
		} else {
			if cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey() == "" {
				responseResult := gjson.GetBytes(resp, "response")
				if responseResult.Exists() {
					resp = []byte(responseResult.Raw)
				}
			}
			_, _ = c.Writer.Write(resp)
			c.Set("API_RESPONSE", resp)
			cliCancel()
			break
		}
	}
}

func (h *GeminiAPIHandlers) handleGeminiGenerateContent(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)

	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()
	backgroundCtx, cliCancel := context.WithCancel(context.Background())
	cliCtx := context.WithValue(backgroundCtx, "gin", c)

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
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			cliCancel()
			return
		}

		template := ""
		parsed := gjson.Parse(string(rawJSON))
		contents := parsed.Get("request.contents")
		if contents.Exists() {
			template = string(rawJSON)
		} else {
			template = `{"project":"","request":{},"model":""}`
			template, _ = sjson.SetRaw(template, "request", string(rawJSON))
			template, _ = sjson.Set(template, "model", gjson.Get(template, "request.model").String())
			template, _ = sjson.Delete(template, "request.model")
		}

		template, errFixCLIToolResponse := translatorGeminiToGeminiCli.FixCLIToolResponse(template)
		if errFixCLIToolResponse != nil {
			c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: errFixCLIToolResponse.Error(),
					Type:    "server_error",
				},
			})
			cliCancel()
			return
		}

		systemInstructionResult := gjson.Get(template, "request.system_instruction")
		if systemInstructionResult.Exists() {
			template, _ = sjson.SetRaw(template, "request.systemInstruction", systemInstructionResult.Raw)
			template, _ = sjson.Delete(template, "request.system_instruction")
		}
		rawJSON = []byte(template)

		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request cli use account: %s, project id: %s", cliClient.(*client.GeminiClient).GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
		}
		resp, err := cliClient.SendRawMessage(cliCtx, rawJSON, alt)
		if err != nil {
			if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
				continue
			} else {
				c.Status(err.StatusCode)
				_, _ = c.Writer.Write([]byte(err.Error.Error()))
				c.Set("API_RESPONSE", []byte(err.Error.Error()))
				cliCancel()
			}
			break
		} else {
			if cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey() == "" {
				responseResult := gjson.GetBytes(resp, "response")
				if responseResult.Exists() {
					resp = []byte(responseResult.Raw)
				}
			}
			_, _ = c.Writer.Write(resp)
			c.Set("API_RESPONSE", resp)
			cliCancel()
			break
		}
	}
}

func (h *GeminiAPIHandlers) handleCodexStreamGenerateContent(c *gin.Context, rawJSON []byte) {
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
	newRequestJSON := translatorGeminiToCodex.ConvertGeminiRequestToCodex(rawJSON)
	// log.Debugf("Request: %s", newRequestJSON)

	modelName := gjson.GetBytes(rawJSON, "model")

	backgroundCtx, cliCancel := context.WithCancel(context.Background())
	cliCtx := context.WithValue(backgroundCtx, "gin", c)

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
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			flusher.Flush()
			cliCancel()
			return
		}

		log.Debugf("Request codex use account: %s", cliClient.GetEmail())

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, []byte(newRequestJSON), "")

		apiResponseData := make([]byte, 0)

		params := &translatorGeminiToCodex.ConvertCodexResponseToGeminiParams{
			Model:             modelName.String(),
			CreatedAt:         0,
			ResponseID:        "",
			LastStorageOutput: "",
		}
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("CodexClient disconnected: %v", c.Request.Context().Err())
					c.Set("API_RESPONSE", apiResponseData)
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					c.Set("API_RESPONSE", apiResponseData)
					cliCancel()
					return
				}
				apiResponseData = append(apiResponseData, chunk...)

				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					data := gjson.ParseBytes(jsonData)
					typeResult := data.Get("type")
					if typeResult.String() != "" {
						outputs := translatorGeminiToCodex.ConvertCodexResponseToGemini(jsonData, params)
						if len(outputs) > 0 {
							for i := 0; i < len(outputs); i++ {
								_, _ = c.Writer.Write([]byte("data: "))
								_, _ = c.Writer.Write([]byte(outputs[i]))
								_, _ = c.Writer.Write([]byte("\n\n"))
							}
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
						c.Set("API_RESPONSE", []byte(err.Error.Error()))
						cliCancel()
					}
					return
				}
			// Send a keep-alive signal to the client.
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func (h *GeminiAPIHandlers) handleCodexGenerateContent(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	// Prepare the request for the backend client.
	newRequestJSON := translatorGeminiToCodex.ConvertGeminiRequestToCodex(rawJSON)
	// log.Debugf("Request: %s", newRequestJSON)

	modelName := gjson.GetBytes(rawJSON, "model")

	backgroundCtx, cliCancel := context.WithCancel(context.Background())
	cliCtx := context.WithValue(backgroundCtx, "gin", c)

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
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			cliCancel()
			return
		}

		log.Debugf("Request codex use account: %s", cliClient.GetEmail())

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, []byte(newRequestJSON), "")
		apiResponseData := make([]byte, 0)
		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("CodexClient disconnected: %v", c.Request.Context().Err())
					c.Set("API_RESPONSE", apiResponseData)
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					c.Set("API_RESPONSE", apiResponseData)
					cliCancel()
					return
				}
				apiResponseData = append(apiResponseData, chunk...)

				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					data := gjson.ParseBytes(jsonData)
					typeResult := data.Get("type")
					if typeResult.String() != "" {
						var geminiStr string
						geminiStr = translatorGeminiToCodex.ConvertCodexResponseToGeminiNonStream(jsonData, modelName.String())
						if geminiStr != "" {
							_, _ = c.Writer.Write([]byte(geminiStr))
						}
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
						c.Set("API_RESPONSE", []byte(err.Error.Error()))
						cliCancel()
					}
					return
				}
			// Send a keep-alive signal to the client.
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}
