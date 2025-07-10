package api

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"io"
	"net/http"
	"time"
)

func (h *APIHandlers) CLIHandler(c *gin.Context) {
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

		httpClient, err := util.SetProxy(h.cfg, &http.Client{})
		if err != nil {
			log.Fatalf("set proxy failed: %v", err)
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
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.getClient(modelName)
		if errorResponse != nil {
			c.Status(errorResponse.StatusCode)
			_, _ = fmt.Fprint(c.Writer, errorResponse.Error)
			flusher.Flush()
			cliCancel()
			return
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
		var errorResponse *client.ErrorMessage
		cliClient, errorResponse = h.getClient(modelName)
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
