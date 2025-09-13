// Package gemini provides HTTP handlers for Gemini CLI API functionality.
// This package implements handlers that process CLI-specific requests for Gemini API operations,
// including content generation and streaming content generation endpoints.
// The handlers restrict access to localhost only and manage communication with the backend service.
package gemini

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/v5/internal/api/handlers"
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// GeminiCLIAPIHandler contains the handlers for Gemini CLI API endpoints.
// It holds a pool of clients to interact with the backend service.
type GeminiCLIAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewGeminiCLIAPIHandler creates a new Gemini CLI API handlers instance.
// It takes an BaseAPIHandler instance as input and returns a GeminiCLIAPIHandler.
func NewGeminiCLIAPIHandler(apiHandlers *handlers.BaseAPIHandler) *GeminiCLIAPIHandler {
	return &GeminiCLIAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the type of this handler.
func (h *GeminiCLIAPIHandler) HandlerType() string {
	return GEMINICLI
}

// Models returns a list of models supported by this handler.
func (h *GeminiCLIAPIHandler) Models() []map[string]any {
	return make([]map[string]any, 0)
}

// CLIHandler handles CLI-specific requests for Gemini API operations.
// It restricts access to localhost only and routes requests to appropriate internal handlers.
func (h *GeminiCLIAPIHandler) CLIHandler(c *gin.Context) {
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

	if requestRawURI == "/v1internal:generateContent" {
		h.handleInternalGenerateContent(c, rawJSON)
	} else if requestRawURI == "/v1internal:streamGenerateContent" {
		h.handleInternalStreamGenerateContent(c, rawJSON)
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

// handleInternalStreamGenerateContent handles streaming content generation requests.
// It sets up a server-sent event stream and forwards the request to the backend client.
// The function continuously proxies response chunks from the backend to the client.
func (h *GeminiCLIAPIHandler) handleInternalStreamGenerateContent(c *gin.Context, rawJSON []byte) {
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

	var errorResponse *interfaces.ErrorMessage
	retryCount := 0
outLoop:
	for retryCount <= h.Cfg.RequestRetry {
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			flusher.Flush()
			cliCancel()
			return
		}

		// Send the message and receive response chunks and errors via channels.
		respChan, errChan := cliClient.SendRawMessageStream(cliCtx, modelName, rawJSON, "")

		for {
			select {
			// Handle client disconnection.
			case <-c.Request.Context().Done():
				if c.Request.Context().Err().Error() == "context canceled" {
					log.Debugf("gemini cli client disconnected: %v", c.Request.Context().Err())
					cliCancel() // Cancel the backend request.
					return
				}
			// Process incoming response chunks.
			case chunk, okStream := <-respChan:
				if !okStream {
					cliCancel()
					return
				}
				_, _ = c.Writer.Write([]byte("data: "))
				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n\n"))

				flusher.Flush()
			// Handle errors from the backend.
			case err, okError := <-errChan:
				if okError {
					errorResponse = err
					h.LoggingAPIResponseError(cliCtx, err)

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
	if errorResponse != nil {
		c.Status(errorResponse.StatusCode)
		_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
		flusher.Flush()
		cliCancel(errorResponse.Error)
		return
	}
}

// handleInternalGenerateContent handles non-streaming content generation requests.
// It sends a request to the backend client and proxies the entire response back to the client at once.
func (h *GeminiCLIAPIHandler) handleInternalGenerateContent(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	var cliClient interfaces.Client
	defer func() {
		if cliClient != nil {
			if mutex := cliClient.GetRequestMutex(); mutex != nil {
				mutex.Unlock()
			}
		}
	}()

	var errorResponse *interfaces.ErrorMessage
	retryCount := 0
	for retryCount <= h.Cfg.RequestRetry {
		cliClient, errorResponse = h.GetClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error.Error())
			cliCancel()
			return
		}

		resp, err := cliClient.SendRawMessage(cliCtx, modelName, rawJSON, "")
		if err != nil {
			errorResponse = err
			h.LoggingAPIResponseError(cliCtx, err)

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
			case 401:
				log.Debugf("unauthorized request, try to refresh token, %s", util.HideAPIKey(cliClient.GetEmail()))
				errRefreshTokens := cliClient.RefreshTokens(cliCtx)
				if errRefreshTokens != nil {
					log.Debugf("refresh token failed, switch client, %s", util.HideAPIKey(cliClient.GetEmail()))
				}
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
			cliCancel()
			break
		}
	}
	if errorResponse != nil {
		c.Status(errorResponse.StatusCode)
		_, _ = c.Writer.Write([]byte(errorResponse.Error.Error()))
		cliCancel(errorResponse.Error)
		return
	}

}
