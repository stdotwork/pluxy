package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/luispater/CLIProxyAPI/internal/client"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	mutex               = &sync.Mutex{}
	lastUsedClientIndex = 0
)

// APIHandlers contains the handlers for API endpoints
type APIHandlers struct {
	cliClients []*client.Client
	debug      bool
}

// NewAPIHandlers creates a new API handlers instance
func NewAPIHandlers(cliClients []*client.Client, debug bool) *APIHandlers {
	return &APIHandlers{
		cliClients: cliClients,
		debug:      debug,
	}
}

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
				"name":                  "Gemini 2.5 Pro Preview",
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

// ChatCompletions handles the /v1/chat/completions endpoint
func (h *APIHandlers) ChatCompletions(c *gin.Context) {
	rawJson, err := c.GetRawData()
	// If data retrieval fails, return 400 error
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request: %v", err), "code": 400})
		return
	}

	streamResult := gjson.GetBytes(rawJson, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJson)
	} else {
		h.handleNonStreamingResponse(c, rawJson)
	}
}

func (h *APIHandlers) prepareRequest(rawJson []byte) (string, []client.Content, []client.ToolDeclaration) {
	// log.Debug(string(rawJson))
	modelName := "gemini-2.5-pro"
	modelResult := gjson.GetBytes(rawJson, "model")
	if modelResult.Type == gjson.String {
		modelName = modelResult.String()
	}

	contents := make([]client.Content, 0)
	messagesResult := gjson.GetBytes(rawJson, "messages")
	if messagesResult.IsArray() {
		messagesResults := messagesResult.Array()
		for i := 0; i < len(messagesResults); i++ {
			messageResult := messagesResults[i]
			roleResult := messageResult.Get("role")
			contentResult := messageResult.Get("content")
			if roleResult.Type == gjson.String {
				if roleResult.String() == "system" {
					if contentResult.Type == gjson.String {
						contents = append(contents, client.Content{Role: "user", Parts: []client.Part{{Text: contentResult.String()}}})
					} else if contentResult.IsObject() {
						contentTypeResult := contentResult.Get("type")
						if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
							contentTextResult := contentResult.Get("text")
							if contentTextResult.Type == gjson.String {
								contents = append(contents, client.Content{Role: "user", Parts: []client.Part{{Text: contentTextResult.String()}}})
								contents = append(contents, client.Content{Role: "model", Parts: []client.Part{{Text: "Understood. I will follow these instructions and use my tools to assist you."}}})
							}
						}
					}
				} else if roleResult.String() == "user" {
					if contentResult.Type == gjson.String {
						contents = append(contents, client.Content{Role: "user", Parts: []client.Part{{Text: contentResult.String()}}})
					} else if contentResult.IsObject() {
						contentTypeResult := contentResult.Get("type")
						if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
							contentTextResult := contentResult.Get("text")
							if contentTextResult.Type == gjson.String {
								contents = append(contents, client.Content{Role: "user", Parts: []client.Part{{Text: contentTextResult.String()}}})
							}
						}
					} else if contentResult.IsArray() {
						contentItemResults := contentResult.Array()
						parts := make([]client.Part, 0)
						for j := 0; j < len(contentItemResults); j++ {
							contentItemResult := contentItemResults[j]
							contentTypeResult := contentItemResult.Get("type")
							if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
								contentTextResult := contentItemResult.Get("text")
								if contentTextResult.Type == gjson.String {
									parts = append(parts, client.Part{Text: contentTextResult.String()})
								}
							} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "image_url" {
								imageURLResult := contentItemResult.Get("image_url.url")
								if imageURLResult.Type == gjson.String {
									imageURL := imageURLResult.String()
									if len(imageURL) > 5 {
										imageURLs := strings.SplitN(imageURL[5:], ";", 2)
										if len(imageURLs) == 2 {
											if len(imageURLs[1]) > 7 {
												parts = append(parts, client.Part{InlineData: &client.InlineData{
													MimeType: imageURLs[0],
													Data:     imageURLs[1][7:],
												}})
											}
										}
									}
								}
							} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "file" {
								filenameResult := contentItemResult.Get("file.filename")
								fileDataResult := contentItemResult.Get("file.file_data")
								if filenameResult.Type == gjson.String && fileDataResult.Type == gjson.String {
									filename := filenameResult.String()
									splitFilename := strings.Split(filename, ".")
									ext := splitFilename[len(splitFilename)-1]

									mimeType, ok := MimeTypes[ext]
									if !ok {
										log.Warnf("Unknown file name extension '%s' at index %d, skipping file", ext, j)
										continue
									}

									parts = append(parts, client.Part{InlineData: &client.InlineData{
										MimeType: mimeType,
										Data:     fileDataResult.String(),
									}})
								}
							}
						}
						contents = append(contents, client.Content{Role: "user", Parts: parts})
					}
				} else if roleResult.String() == "assistant" {
					if contentResult.Type == gjson.String {
						contents = append(contents, client.Content{Role: "model", Parts: []client.Part{{Text: contentResult.String()}}})
					} else if contentResult.IsObject() {
						contentTypeResult := contentResult.Get("type")
						if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
							contentTextResult := contentResult.Get("text")
							if contentTextResult.Type == gjson.String {
								contents = append(contents, client.Content{Role: "user", Parts: []client.Part{{Text: contentTextResult.String()}}})
							}
						}
					} else if !contentResult.Exists() || contentResult.Type == gjson.Null {
						toolCallsResult := messageResult.Get("tool_calls")
						if toolCallsResult.IsArray() {
							tcsResult := toolCallsResult.Array()
							for j := 0; j < len(tcsResult); j++ {
								tcResult := tcsResult[j]
								functionNameResult := tcResult.Get("function.name")
								functionArguments := tcResult.Get("function.arguments")
								if functionNameResult.Exists() && functionNameResult.Type == gjson.String && functionArguments.Exists() && functionArguments.Type == gjson.String {
									var args map[string]any
									err := json.Unmarshal([]byte(functionArguments.String()), &args)
									if err == nil {
										contents = append(contents, client.Content{
											Role: "model", Parts: []client.Part{
												{
													FunctionCall: &client.FunctionCall{
														Name: functionNameResult.String(),
														Args: args,
													},
												},
											},
										})
									}
								}
							}
						}
					}
				} else if roleResult.String() == "tool" {
					toolCallIDResult := messageResult.Get("tool_call_id")
					if toolCallIDResult.Exists() && toolCallIDResult.Type == gjson.String {
						if contentResult.Type == gjson.String {
							functionResponse := client.FunctionResponse{Name: toolCallIDResult.String(), Response: map[string]interface{}{"result": contentResult.String()}}
							contents = append(contents, client.Content{Role: "tool", Parts: []client.Part{{FunctionResponse: &functionResponse}}})
						} else if contentResult.IsObject() {
							contentTypeResult := contentResult.Get("type")
							if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
								contentTextResult := contentResult.Get("text")
								if contentTextResult.Type == gjson.String {
									functionResponse := client.FunctionResponse{Name: toolCallIDResult.String(), Response: map[string]interface{}{"result": contentResult.String()}}
									contents = append(contents, client.Content{Role: "tool", Parts: []client.Part{{FunctionResponse: &functionResponse}}})
								}
							}
						}
					}
				}
			}
		}
	}

	var tools []client.ToolDeclaration
	toolsResult := gjson.GetBytes(rawJson, "tools")
	if toolsResult.IsArray() {
		tools = make([]client.ToolDeclaration, 1)
		tools[0].FunctionDeclarations = make([]any, 0)
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolTypeResult := toolsResults[i].Get("type")
			if toolTypeResult.Type != gjson.String || toolTypeResult.String() != "function" {
				continue
			}
			functionTypeResult := toolsResults[i].Get("function")
			if functionTypeResult.Exists() && functionTypeResult.IsObject() {
				var functionDeclaration any
				err := json.Unmarshal([]byte(functionTypeResult.Raw), &functionDeclaration)
				if err == nil {
					tools[0].FunctionDeclarations = append(tools[0].FunctionDeclarations, functionDeclaration)
				}
			}
		}
	} else {
		tools = make([]client.ToolDeclaration, 0)
	}
	return modelName, contents, tools
}

// handleNonStreamingResponse handles non-streaming responses
func (h *APIHandlers) handleNonStreamingResponse(c *gin.Context, rawJson []byte) {
	c.Header("Content-Type", "application/json")

	// Handle streaming manually
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

	modelName, contents, tools := h.prepareRequest(rawJson)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
		}
	}()

	// Lock the mutex to update the last used page index
	mutex.Lock()
	startIndex := lastUsedClientIndex
	currentIndex := (startIndex + 1) % len(h.cliClients)
	lastUsedClientIndex = currentIndex
	mutex.Unlock()

	// Reorder the pages to start from the last used index
	reorderedPages := make([]*client.Client, len(h.cliClients))
	for i := 0; i < len(h.cliClients); i++ {
		reorderedPages[i] = h.cliClients[(startIndex+1+i)%len(h.cliClients)]
	}

	locked := false
	for i := 0; i < len(reorderedPages); i++ {
		cliClient = reorderedPages[i]
		if cliClient.RequestMutex.TryLock() {
			locked = true
			break
		}
	}
	if !locked {
		cliClient = h.cliClients[0]
		cliClient.RequestMutex.Lock()
	}

	log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
	jsonTemplate := `{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`
	respChan, errChan := cliClient.SendMessageStream(cliCtx, rawJson, modelName, contents, tools)
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
				_, _ = fmt.Fprint(c.Writer, jsonTemplate)
				flusher.Flush()
				cliCancel()
				return
			} else {
				jsonTemplate = h.convertCliToOpenAINonStream(jsonTemplate, chunk)
			}
		case err, okError := <-errChan:
			if okError {
				c.Status(err.StatusCode)
				_, _ = fmt.Fprint(c.Writer, err.Error.Error())
				flusher.Flush()
				// c.JSON(http.StatusInternalServerError, ErrorResponse{
				// 	Error: ErrorDetail{
				// 		Message: err.Error(),
				// 		Type:    "server_error",
				// 	},
				// })
				cliCancel()
				return
			}
		case <-time.After(500 * time.Millisecond):
			_, _ = c.Writer.Write([]byte("\n"))
			flusher.Flush()
		}
	}
}

// handleStreamingResponse handles streaming responses
func (h *APIHandlers) handleStreamingResponse(c *gin.Context, rawJson []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	// Handle streaming manually
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
	modelName, contents, tools := h.prepareRequest(rawJson)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	var cliClient *client.Client
	defer func() {
		if cliClient != nil {
			cliClient.RequestMutex.Unlock()
		}
	}()

	// Lock the mutex to update the last used page index
	mutex.Lock()
	startIndex := lastUsedClientIndex
	currentIndex := (startIndex + 1) % len(h.cliClients)
	lastUsedClientIndex = currentIndex
	mutex.Unlock()

	// Reorder the pages to start from the last used index
	reorderedPages := make([]*client.Client, len(h.cliClients))
	for i := 0; i < len(h.cliClients); i++ {
		reorderedPages[i] = h.cliClients[(startIndex+1+i)%len(h.cliClients)]
	}

	locked := false
	for i := 0; i < len(reorderedPages); i++ {
		cliClient = reorderedPages[i]
		if cliClient.RequestMutex.TryLock() {
			locked = true
			break
		}
	}
	if !locked {
		cliClient = h.cliClients[0]
		cliClient.RequestMutex.Lock()
	}

	log.Debugf("Request use account: %s, project id: %s", cliClient.GetEmail(), cliClient.GetProjectID())
	respChan, errChan := cliClient.SendMessageStream(cliCtx, rawJson, modelName, contents, tools)
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
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cliCancel()
				return
			} else {
				openAIFormat := h.convertCliToOpenAI(chunk)
				if openAIFormat != "" {
					_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", openAIFormat)
					flusher.Flush()
				}
			}
		case err, okError := <-errChan:
			if okError {
				c.Status(err.StatusCode)
				_, _ = fmt.Fprint(c.Writer, err.Error.Error())
				flusher.Flush()
				// c.JSON(http.StatusInternalServerError, ErrorResponse{
				// 	Error: ErrorDetail{
				// 		Message: err.Error(),
				// 		Type:    "server_error",
				// 	},
				// })
				cliCancel()
				return
			}
		case <-time.After(500 * time.Millisecond):
			_, _ = c.Writer.Write([]byte(": CLI-PROXY-API PROCESSING\n\n"))
			flusher.Flush()
		}
	}
}

func (h *APIHandlers) convertCliToOpenAI(rawJson []byte) string {
	// log.Debugf(string(rawJson))
	template := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	modelVersionResult := gjson.GetBytes(rawJson, "response.modelVersion")
	if modelVersionResult.Exists() && modelVersionResult.Type == gjson.String {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	createTimeResult := gjson.GetBytes(rawJson, "response.createTime")
	if createTimeResult.Exists() && createTimeResult.Type == gjson.String {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		var unixTimestamp int64
		if err == nil {
			unixTimestamp = t.Unix()
		} else {
			unixTimestamp = time.Now().Unix()
		}
		template, _ = sjson.Set(template, "created", unixTimestamp)
	}

	responseIdResult := gjson.GetBytes(rawJson, "response.responseId")
	if responseIdResult.Exists() && responseIdResult.Type == gjson.String {
		template, _ = sjson.Set(template, "id", responseIdResult.String())
	}

	finishReasonResult := gjson.GetBytes(rawJson, "response.candidates.0.finishReason")
	if finishReasonResult.Exists() && finishReasonResult.Type == gjson.String {
		template, _ = sjson.Set(template, "choices.0.finish_reason", finishReasonResult.String())
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", finishReasonResult.String())
	}

	usageResult := gjson.GetBytes(rawJson, "response.usageMetadata")
	candidatesTokenCountResult := usageResult.Get("candidatesTokenCount")
	if candidatesTokenCountResult.Exists() && candidatesTokenCountResult.Type == gjson.Number {
		template, _ = sjson.Set(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
	}
	totalTokenCountResult := usageResult.Get("totalTokenCount")
	if totalTokenCountResult.Exists() && totalTokenCountResult.Type == gjson.Number {
		template, _ = sjson.Set(template, "usage.total_tokens", totalTokenCountResult.Int())
	}
	thoughtsTokenCountResult := usageResult.Get("thoughtsTokenCount")
	promptTokenCountResult := usageResult.Get("promptTokenCount")
	if promptTokenCountResult.Exists() && promptTokenCountResult.Type == gjson.Number {
		if thoughtsTokenCountResult.Exists() && thoughtsTokenCountResult.Type == gjson.Number {
			template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCountResult.Int()+thoughtsTokenCountResult.Int())
		} else {
			template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCountResult.Int())
		}
	}
	if thoughtsTokenCountResult.Exists() && thoughtsTokenCountResult.Type == gjson.Number {
		template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCountResult.Int())
	}

	partResult := gjson.GetBytes(rawJson, "response.candidates.0.content.parts.0")
	partTextResult := partResult.Get("text")
	functionCallResult := partResult.Get("functionCall")

	if partTextResult.Exists() && partTextResult.Type == gjson.String {
		partThoughtResult := partResult.Get("thought")
		if partThoughtResult.Exists() && partThoughtResult.Type == gjson.True {
			template, _ = sjson.Set(template, "choices.0.delta.reasoning_content", partTextResult.String())
		} else {
			template, _ = sjson.Set(template, "choices.0.delta.content", partTextResult.String())
		}
		template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
	} else if functionCallResult.Exists() {
		functionCallTemplate := `[{"id": "","type": "function","function": {"name": "","arguments": ""}}]`
		fcNameResult := functionCallResult.Get("name")
		if fcNameResult.Exists() && fcNameResult.Type == gjson.String {
			functionCallTemplate, _ = sjson.Set(functionCallTemplate, "0.id", fcNameResult.String())
			functionCallTemplate, _ = sjson.Set(functionCallTemplate, "0.function.name", fcNameResult.String())
		}
		fcArgsResult := functionCallResult.Get("args")
		if fcArgsResult.Exists() && fcArgsResult.IsObject() {
			functionCallTemplate, _ = sjson.Set(functionCallTemplate, "0.function.arguments", fcArgsResult.Raw)
		}
		template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls", functionCallTemplate)
	} else {
		return ""
	}

	return template
}

func (h *APIHandlers) convertCliToOpenAINonStream(template string, rawJson []byte) string {
	modelVersionResult := gjson.GetBytes(rawJson, "response.modelVersion")
	if modelVersionResult.Exists() && modelVersionResult.Type == gjson.String {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	createTimeResult := gjson.GetBytes(rawJson, "response.createTime")
	if createTimeResult.Exists() && createTimeResult.Type == gjson.String {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		var unixTimestamp int64
		if err == nil {
			unixTimestamp = t.Unix()
		} else {
			unixTimestamp = time.Now().Unix()
		}
		template, _ = sjson.Set(template, "created", unixTimestamp)
	}

	responseIdResult := gjson.GetBytes(rawJson, "response.responseId")
	if responseIdResult.Exists() && responseIdResult.Type == gjson.String {
		template, _ = sjson.Set(template, "id", responseIdResult.String())
	}

	finishReasonResult := gjson.GetBytes(rawJson, "response.candidates.0.finishReason")
	if finishReasonResult.Exists() && finishReasonResult.Type == gjson.String {
		template, _ = sjson.Set(template, "choices.0.finish_reason", finishReasonResult.String())
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", finishReasonResult.String())
	}

	usageResult := gjson.GetBytes(rawJson, "response.usageMetadata")
	candidatesTokenCountResult := usageResult.Get("candidatesTokenCount")
	if candidatesTokenCountResult.Exists() && candidatesTokenCountResult.Type == gjson.Number {
		template, _ = sjson.Set(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
	}
	totalTokenCountResult := usageResult.Get("totalTokenCount")
	if totalTokenCountResult.Exists() && totalTokenCountResult.Type == gjson.Number {
		template, _ = sjson.Set(template, "usage.total_tokens", totalTokenCountResult.Int())
	}
	thoughtsTokenCountResult := usageResult.Get("thoughtsTokenCount")
	promptTokenCountResult := usageResult.Get("promptTokenCount")
	if promptTokenCountResult.Exists() && promptTokenCountResult.Type == gjson.Number {
		if thoughtsTokenCountResult.Exists() && thoughtsTokenCountResult.Type == gjson.Number {
			template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCountResult.Int()+thoughtsTokenCountResult.Int())
		} else {
			template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCountResult.Int())
		}
	}
	if thoughtsTokenCountResult.Exists() && thoughtsTokenCountResult.Type == gjson.Number {
		template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCountResult.Int())
	}

	partResult := gjson.GetBytes(rawJson, "response.candidates.0.content.parts.0")
	partTextResult := partResult.Get("text")
	functionCallResult := partResult.Get("functionCall")

	if partTextResult.Exists() && partTextResult.Type == gjson.String {
		partThoughtResult := partResult.Get("thought")
		if partThoughtResult.Exists() && partThoughtResult.Type == gjson.True {
			reasoningContentResult := gjson.Get(template, "choices.0.message.reasoning_content")
			if reasoningContentResult.Type == gjson.String {
				template, _ = sjson.Set(template, "choices.0.message.reasoning_content", reasoningContentResult.String()+partTextResult.String())
			} else {
				template, _ = sjson.Set(template, "choices.0.message.reasoning_content", partTextResult.String())
			}
		} else {
			reasoningContentResult := gjson.Get(template, "choices.0.message.content")
			if reasoningContentResult.Type == gjson.String {
				template, _ = sjson.Set(template, "choices.0.message.content", reasoningContentResult.String()+partTextResult.String())
			} else {
				template, _ = sjson.Set(template, "choices.0.message.content", partTextResult.String())
			}
		}
		template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
	} else if functionCallResult.Exists() {
		toolCallsResult := gjson.Get(template, "choices.0.message.tool_calls")
		if !toolCallsResult.Exists() || toolCallsResult.Type == gjson.Null {
			template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls", `[]`)
		}

		functionCallItemTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`
		fcNameResult := functionCallResult.Get("name")
		if fcNameResult.Exists() && fcNameResult.Type == gjson.String {
			functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "id", fcNameResult.String())
			functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.name", fcNameResult.String())
		}
		fcArgsResult := functionCallResult.Get("args")
		if fcArgsResult.Exists() && fcArgsResult.IsObject() {
			functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.arguments", fcArgsResult.Raw)
		}
		template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
		template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls.-1", functionCallItemTemplate)
	} else {
		return ""
	}

	return template
}
