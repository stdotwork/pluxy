// Package cli provides HTTP handlers for Gemini CLI API functionality.
// This package implements handlers that process CLI-specific requests for Gemini API operations,
// including content generation and streaming content generation endpoints.
// The handlers restrict access to localhost only and manage communication with the backend service.
package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	"github.com/luispater/CLIProxyAPI/internal/client"
	translatorGeminiToCodex "github.com/luispater/CLIProxyAPI/internal/translator/codex/gemini"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// GeminiCLIAPIHandlers contains the handlers for Gemini CLI API endpoints.
// It holds a pool of clients to interact with the backend service.
type GeminiCLIAPIHandlers struct {
	*handlers.APIHandlers
}

// NewGeminiCLIAPIHandlers creates a new Gemini CLI API handlers instance.
// It takes an APIHandlers instance as input and returns a GeminiCLIAPIHandlers.
func NewGeminiCLIAPIHandlers(apiHandlers *handlers.APIHandlers) *GeminiCLIAPIHandlers {
	return &GeminiCLIAPIHandlers{
		APIHandlers: apiHandlers,
	}
}

// CLIHandler handles CLI-specific requests for Gemini API operations.
// It restricts access to localhost only and routes requests to appropriate internal handlers.
func (h *GeminiCLIAPIHandlers) CLIHandler(c *gin.Context) {
	if !strings.HasPrefix(c.Request.RemoteAddr, "127.0.0.1:") {
		c.JSON(http.StatusForbidden, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "CLI reply only allow local access",
				Type:    "forbidden",
			},
		})
		return
	}

	rawJSON, _ := c.GetRawData()
	requestRawURI := c.Request.URL.Path

	modelName := gjson.GetBytes(rawJSON, "model")
	provider := util.GetProviderName(modelName.String())

	if requestRawURI == "/v1internal:generateContent" {
		if provider == "gemini" || provider == "unknow" {
			h.handleInternalGenerateContent(c, rawJSON)
		} else if provider == "gpt" {
			h.handleCodexInternalGenerateContent(c, rawJSON)
		}
	} else if requestRawURI == "/v1internal:streamGenerateContent" {
		if provider == "gemini" || provider == "unknow" {
			h.handleInternalStreamGenerateContent(c, rawJSON)
		} else if provider == "gpt" {
			h.handleCodexInternalStreamGenerateContent(c, rawJSON)
		}
	} else {
		reqBody := bytes.NewBuffer(rawJSON)
		req, err := http.NewRequest("POST", fmt.Sprintf("https://cloudcode-pa.googleapis.com%s", c.Request.URL.RequestURI()), reqBody)
		if err != nil {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", err),
					Type:    "invalid_request_error",
				},
			})
			return
		}
		for key, value := range c.Request.Header {
			req.Header[key] = value
		}

		httpClient := util.SetProxy(h.Cfg, &http.Client{})

		resp, err := httpClient.Do(req)
		if err != nil {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
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

			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
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
		c.Set("API_RESPONSE", output)
	}
}

func (h *GeminiCLIAPIHandlers) handleInternalStreamGenerateContent(c *gin.Context, rawJSON []byte) {
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

		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request cli use account: %s, project id: %s", cliClient.(*client.GeminiClient).GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
		}
		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, rawJSON, "")
		hasFirstResponse := false

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

				hasFirstResponse = true
				if cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey() != "" {
					chunk, _ = sjson.SetRawBytes(chunk, "response", chunk)
				}
				_, _ = c.Writer.Write([]byte("data: "))
				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n\n"))

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
				if hasFirstResponse {
					_, _ = c.Writer.Write([]byte("\n"))
					flusher.Flush()
				}
			}
		}
	}
}

func (h *GeminiCLIAPIHandlers) handleInternalGenerateContent(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	// log.Debugf("GenerateContent: %s", string(rawJSON))
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

		if glAPIKey := cliClient.(*client.GeminiClient).GetGenerativeLanguageAPIKey(); glAPIKey != "" {
			log.Debugf("Request use generative language API Key: %s", glAPIKey)
		} else {
			log.Debugf("Request cli use account: %s, project id: %s", cliClient.(*client.GeminiClient).GetEmail(), cliClient.(*client.GeminiClient).GetProjectID())
		}

		resp, err := cliClient.SendRawMessage(cliCtx, rawJSON, "")
		if err != nil {
			if err.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
				continue
			} else {
				c.Status(err.StatusCode)
				_, _ = c.Writer.Write([]byte(err.Error.Error()))
				log.Debugf("code: %d, error: %s", err.StatusCode, err.Error.Error())
				c.Set("API_RESPONSE", []byte(err.Error.Error()))
				cliCancel()
			}
			break
		} else {
			_, _ = c.Writer.Write(resp)
			c.Set("API_RESPONSE", resp)
			cliCancel()
			break
		}
	}
}

func (h *GeminiCLIAPIHandlers) handleCodexInternalStreamGenerateContent(c *gin.Context, rawJSON []byte) {
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

	modelResult := gjson.GetBytes(rawJSON, "model")
	rawJSON = []byte(gjson.GetBytes(rawJSON, "request").Raw)
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelResult.String())
	rawJSON, _ = sjson.SetRawBytes(rawJSON, "system_instruction", []byte(gjson.GetBytes(rawJSON, "systemInstruction").Raw))
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "systemInstruction")

	// log.Debugf("Request: %s", string(rawJSON))
	// return

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

		params := &translatorGeminiToCodex.ConvertCodexResponseToGeminiParams{
			Model:             modelName.String(),
			CreatedAt:         0,
			ResponseID:        "",
			LastStorageOutput: "",
		}
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
				// _, _ = logFile.Write(chunk)
				// _, _ = logFile.Write([]byte("\n"))
				apiResponseData = append(apiResponseData, chunk...)
				if bytes.HasPrefix(chunk, []byte("data: ")) {
					jsonData := chunk[6:]
					data := gjson.ParseBytes(jsonData)
					typeResult := data.Get("type")
					if typeResult.String() != "" {
						outputs := translatorGeminiToCodex.ConvertCodexResponseToGemini(jsonData, params)
						if len(outputs) > 0 {
							for i := 0; i < len(outputs); i++ {
								outputs[i], _ = sjson.SetRaw("{}", "response", outputs[i])
								_, _ = c.Writer.Write([]byte("data: "))
								_, _ = c.Writer.Write([]byte(outputs[i]))
								_, _ = c.Writer.Write([]byte("\n\n"))
							}
						}
					}
				}
				flusher.Flush()
			// Handle errors from the backend.
			case errMessage, okError := <-errChan:
				if okError {
					if errMessage.StatusCode == 429 && h.Cfg.QuotaExceeded.SwitchProject {
						continue outLoop
					} else {
						log.Debugf("code: %d, error: %s", errMessage.StatusCode, errMessage.Error.Error())
						c.Status(errMessage.StatusCode)
						_, _ = fmt.Fprint(c.Writer, errMessage.Error.Error())
						flusher.Flush()
						c.Set("API_RESPONSE", []byte(errMessage.Error.Error()))
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

func (h *GeminiCLIAPIHandlers) handleCodexInternalGenerateContent(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	orgRawJSON := rawJSON
	modelResult := gjson.GetBytes(rawJSON, "model")
	rawJSON = []byte(gjson.GetBytes(rawJSON, "request").Raw)
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelResult.String())
	rawJSON, _ = sjson.SetRawBytes(rawJSON, "system_instruction", []byte(gjson.GetBytes(rawJSON, "systemInstruction").Raw))
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "systemInstruction")

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
						log.Debugf("org: %s", string(orgRawJSON))
						log.Debugf("raw: %s", string(rawJSON))
						log.Debugf("newRequestJSON: %s", newRequestJSON)
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
