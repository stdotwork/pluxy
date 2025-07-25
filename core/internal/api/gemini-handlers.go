package api

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/translator"
	"github.com/luispater/CLIProxyAPI/internal/client"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"net/http"
	"strings"
	"time"
)

func (h *APIHandlers) GeminiModels(c *gin.Context) {
	c.Status(200)
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

func (h *APIHandlers) GeminiHandler(c *gin.Context) {
	var person struct {
		Action string `uri:"action" binding:"required"`
	}
	if err := c.ShouldBindUri(&person); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	action := strings.Split(person.Action, ":")
	if len(action) != 2 {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: ErrorDetail{
				Message: fmt.Sprintf("%s not found.", c.Request.URL.Path),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	modelName := action[0]
	method := action[1]
	rawJson, _ := c.GetRawData()
	rawJson, _ = sjson.SetBytes(rawJson, "model", []byte(modelName))

	if method == "generateContent" {
		h.geminiGenerateContent(c, rawJson)
	} else if method == "streamGenerateContent" {
		h.geminiStreamGenerateContent(c, rawJson)
	}
}

func (h *APIHandlers) geminiStreamGenerateContent(c *gin.Context, rawJson []byte) {
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
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.getClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			flusher.Flush()
			cliCancel()
			return
		}

		template := `{"project":"","request":{},"model":""}`
		template, _ = sjson.SetRaw(template, "request", string(rawJson))
		template, _ = sjson.Set(template, "model", gjson.Get(template, "request.model").String())
		template, _ = sjson.Delete(template, "request.model")

		template, errFixCLIToolResponse := translator.FixCLIToolResponse(template)
		if errFixCLIToolResponse != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error: ErrorDetail{
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
		rawJson = []byte(template)

		if glAPIKey := cliClient.GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
		}

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, rawJson)
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
					if cliClient.GetGenerativeLanguageAPIKey() == "" {
						responseResult := gjson.GetBytes(chunk, "response")
						if responseResult.Exists() {
							chunk = []byte(responseResult.Raw)
						}
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
			}
		}
	}
}

func (h *APIHandlers) geminiGenerateContent(c *gin.Context, rawJson []byte) {
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
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.getClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			cliCancel()
			return
		}

		template := `{"project":"","request":{},"model":""}`
		template, _ = sjson.SetRaw(template, "request", string(rawJson))
		template, _ = sjson.Set(template, "model", gjson.Get(template, "request.model").String())
		template, _ = sjson.Delete(template, "request.model")

		template, errFixCLIToolResponse := translator.FixCLIToolResponse(template)
		if errFixCLIToolResponse != nil {
			c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error: ErrorDetail{
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
		rawJson = []byte(template)

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
