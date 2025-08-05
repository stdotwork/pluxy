// Package gemini provides HTTP handlers for Gemini API endpoints.
// This package implements handlers for managing Gemini model operations including
// model listing, content generation, streaming content generation, and token counting.
// It serves as a proxy layer between clients and the Gemini backend service,
// handling request translation, client management, and response processing.
package gemini

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	"github.com/luispater/CLIProxyAPI/internal/api/translator/gemini/cli"
	"github.com/luispater/CLIProxyAPI/internal/client"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"net/http"
	"strings"
	"time"
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
	c.Status(http.StatusOK)
	c.Header("Content-Type", "application/json; charset=UTF-8")
	_, _ = c.Writer.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash","version":"001","displayName":"Gemini `))
	_, _ = c.Writer.Write([]byte(`2.5 Flash","description":"Stable version of Gemini 2.5 Flash, our mid-size multimod`))
	_, _ = c.Writer.Write([]byte(`al model that supports up to 1 million tokens, released in June of 2025.","inputTok`))
	_, _ = c.Writer.Write([]byte(`enLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateCo`))
	_, _ = c.Writer.Write([]byte(`ntent","countTokens","createCachedContent","batchGenerateContent"],"temperature":1,`))
	_, _ = c.Writer.Write([]byte(`"topP":0.95,"topK":64,"maxTemperature":2,"thinking":true},{"name":"models/gemini-2.`))
	_, _ = c.Writer.Write([]byte(`5-pro","version":"2.5","displayName":"Gemini 2.5 Pro","description":"Stable release`))
	_, _ = c.Writer.Write([]byte(` (June 17th, 2025) of Gemini 2.5 Pro","inputTokenLimit":1048576,"outputTokenLimit":`))
	_, _ = c.Writer.Write([]byte(`65536,"supportedGenerationMethods":["generateContent","countTokens","createCachedCo`))
	_, _ = c.Writer.Write([]byte(`ntent","batchGenerateContent"],"temperature":1,"topP":0.95,"topK":64,"maxTemperatur`))
	_, _ = c.Writer.Write([]byte(`e":2,"thinking":true}],"nextPageToken":""}`))
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
	if request.Action == "gemini-2.5-pro" {
		c.Status(http.StatusOK)
		c.Header("Content-Type", "application/json; charset=UTF-8")
		_, _ = c.Writer.Write([]byte(`{"name":"models/gemini-2.5-pro","version":"2.5","displayName":"Gemini 2.5 Pro",`))
		_, _ = c.Writer.Write([]byte(`"description":"Stable release (June 17th, 2025) of Gemini 2.5 Pro","inputTokenL`))
		_, _ = c.Writer.Write([]byte(`imit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateC`))
		_, _ = c.Writer.Write([]byte(`ontent","countTokens","createCachedContent","batchGenerateContent"],"temperatur`))
		_, _ = c.Writer.Write([]byte(`e":1,"topP":0.95,"topK":64,"maxTemperature":2,"thinking":true}`))
	} else if request.Action == "gemini-2.5-flash" {
		c.Status(http.StatusOK)
		c.Header("Content-Type", "application/json; charset=UTF-8")
		_, _ = c.Writer.Write([]byte(`{"name":"models/gemini-2.5-flash","version":"001","displayName":"Gemini 2.5 Fla`))
		_, _ = c.Writer.Write([]byte(`sh","description":"Stable version of Gemini 2.5 Flash, our mid-size multimodal `))
		_, _ = c.Writer.Write([]byte(`model that supports up to 1 million tokens, released in June of 2025.","inputTo`))
		_, _ = c.Writer.Write([]byte(`kenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["gener`))
		_, _ = c.Writer.Write([]byte(`ateContent","countTokens","createCachedContent","batchGenerateContent"],"temper`))
		_, _ = c.Writer.Write([]byte(`ature":1,"topP":0.95,"topK":64,"maxTemperature":2,"thinking":true}`))
	} else {
		c.Status(http.StatusNotFound)
		_, _ = c.Writer.Write([]byte(
			`{"error":{"message":"Not Found","code":404,"status":"NOT_FOUND"}}`,
		))
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

	if method == "generateContent" {
		h.geminiGenerateContent(c, rawJSON)
	} else if method == "streamGenerateContent" {
		h.geminiStreamGenerateContent(c, rawJSON)
	} else if method == "countTokens" {
		h.geminiCountTokens(c, rawJSON)
	}
}

func (h *GeminiAPIHandlers) geminiStreamGenerateContent(c *gin.Context, rawJSON []byte) {
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

		template, errFixCLIToolResponse := cli.FixCLIToolResponse(template)
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

		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, rawJSON, alt)
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
				}
				if cliClient.GetGenerativeLanguageAPIKey() == "" {
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

func (h *GeminiAPIHandlers) geminiCountTokens(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)
	// orgrawJSON := rawJSON
	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
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

		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())

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
				cliCancel()
				// log.Debugf(err.Error.Error())
				// log.Debugf(string(rawJSON))
				// log.Debugf(string(orgrawJSON))
			}
			break
		} else {
			if cliClient.GetGenerativeLanguageAPIKey() == "" {
				responseResult := gjson.GetBytes(resp, "response")
				if responseResult.Exists() {
					resp = []byte(responseResult.Raw)
				}
			}
			_, _ = c.Writer.Write(resp)
			cliCancel()
			break
		}
	}
}

func (h *GeminiAPIHandlers) geminiGenerateContent(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)

	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
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

		template, errFixCLIToolResponse := cli.FixCLIToolResponse(template)
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

		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}
		resp, err := cliClient.SendRawMessage(cliCtx, rawJSON, alt)
		if err != nil {
			if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
				continue
			} else {
				c.Status(err.StatusCode)
				_, _ = c.Writer.Write([]byte(err.Error.Error()))
				cliCancel()
			}
			break
		} else {
			if cliClient.GetGenerativeLanguageAPIKey() == "" {
				responseResult := gjson.GetBytes(resp, "response")
				if responseResult.Exists() {
					resp = []byte(responseResult.Raw)
				}
			}
			_, _ = c.Writer.Write(resp)
			cliCancel()
			break
		}
	}
}
