package translator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/tidwall/sjson"
	"strings"

	"github.com/luispater/CLIProxyAPI/internal/client"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// PrepareRequest translates a raw JSON request from an OpenAI-compatible format
// to the internal format expected by the backend client. It parses messages,
// roles, content types (text, image, file), and tool calls.
func PrepareRequest(rawJson []byte) (string, *client.Content, []client.Content, []client.ToolDeclaration) {
	// Extract the model name from the request, defaulting to "gemini-2.5-pro".
	modelName := "gemini-2.5-pro"
	modelResult := gjson.GetBytes(rawJson, "model")
	if modelResult.Type == gjson.String {
		modelName = modelResult.String()
	}

	// Initialize data structures for processing conversation messages
	// contents: stores the processed conversation history
	// systemInstruction: stores system-level instructions separate from conversation
	contents := make([]client.Content, 0)
	var systemInstruction *client.Content
	messagesResult := gjson.GetBytes(rawJson, "messages")

	// Pre-process tool responses to create a lookup map
	// This first pass collects all tool responses so they can be matched with their corresponding calls
	toolItems := make(map[string]*client.FunctionResponse)
	if messagesResult.IsArray() {
		messagesResults := messagesResult.Array()
		for i := 0; i < len(messagesResults); i++ {
			messageResult := messagesResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				continue
			}
			contentResult := messageResult.Get("content")

			// Extract tool responses for later matching with function calls
			if roleResult.String() == "tool" {
				toolCallID := messageResult.Get("tool_call_id").String()
				if toolCallID != "" {
					var responseData string
					// Handle both string and object-based tool response formats
					if contentResult.Type == gjson.String {
						responseData = contentResult.String()
					} else if contentResult.IsObject() && contentResult.Get("type").String() == "text" {
						responseData = contentResult.Get("text").String()
					}

					// Clean up tool call ID by removing timestamp suffix
					// This normalizes IDs for consistent matching between calls and responses
					toolCallIDs := strings.Split(toolCallID, "-")
					strings.Join(toolCallIDs, "-")
					newToolCallID := strings.Join(toolCallIDs[:len(toolCallIDs)-1], "-")

					// Create function response object with normalized ID and response data
					functionResponse := client.FunctionResponse{Name: newToolCallID, Response: map[string]interface{}{"result": responseData}}
					toolItems[toolCallID] = &functionResponse
				}
			}
		}
	}

	if messagesResult.IsArray() {
		messagesResults := messagesResult.Array()
		for i := 0; i < len(messagesResults); i++ {
			messageResult := messagesResults[i]
			roleResult := messageResult.Get("role")
			contentResult := messageResult.Get("content")
			if roleResult.Type != gjson.String {
				continue
			}

			switch roleResult.String() {
			// System messages are converted to a user message followed by a model's acknowledgment.
			case "system":
				if contentResult.Type == gjson.String {
					systemInstruction = &client.Content{Role: "user", Parts: []client.Part{{Text: contentResult.String()}}}
				} else if contentResult.IsObject() {
					// Handle object-based system messages.
					if contentResult.Get("type").String() == "text" {
						systemInstruction = &client.Content{Role: "user", Parts: []client.Part{{Text: contentResult.Get("text").String()}}}
					}
				}
			// User messages can contain simple text or a multi-part body.
			case "user":
				if contentResult.Type == gjson.String {
					contents = append(contents, client.Content{Role: "user", Parts: []client.Part{{Text: contentResult.String()}}})
				} else if contentResult.IsArray() {
					// Handle multi-part user messages (text, images, files).
					contentItemResults := contentResult.Array()
					parts := make([]client.Part, 0)
					for j := 0; j < len(contentItemResults); j++ {
						contentItemResult := contentItemResults[j]
						contentTypeResult := contentItemResult.Get("type")
						switch contentTypeResult.String() {
						case "text":
							parts = append(parts, client.Part{Text: contentItemResult.Get("text").String()})
						case "image_url":
							// Parse data URI for images.
							imageURL := contentItemResult.Get("image_url.url").String()
							if len(imageURL) > 5 {
								imageURLs := strings.SplitN(imageURL[5:], ";", 2)
								if len(imageURLs) == 2 && len(imageURLs[1]) > 7 {
									parts = append(parts, client.Part{InlineData: &client.InlineData{
										MimeType: imageURLs[0],
										Data:     imageURLs[1][7:],
									}})
								}
							}
						case "file":
							// Handle file attachments by determining MIME type from extension.
							filename := contentItemResult.Get("file.filename").String()
							fileData := contentItemResult.Get("file.file_data").String()
							ext := ""
							if split := strings.Split(filename, "."); len(split) > 1 {
								ext = split[len(split)-1]
							}
							if mimeType, ok := MimeTypes[ext]; ok {
								parts = append(parts, client.Part{InlineData: &client.InlineData{
									MimeType: mimeType,
									Data:     fileData,
								}})
							} else {
								log.Warnf("Unknown file name extension '%s' at index %d, skipping file", ext, j)
							}
						}
					}
					contents = append(contents, client.Content{Role: "user", Parts: parts})
				}
			// Assistant messages can contain text responses or tool calls
			// In the internal format, assistant messages are converted to "model" role
			case "assistant":
				if contentResult.Type == gjson.String {
					// Simple text response from the assistant
					contents = append(contents, client.Content{Role: "model", Parts: []client.Part{{Text: contentResult.String()}}})
				} else if !contentResult.Exists() || contentResult.Type == gjson.Null {
					// Handle complex tool calls made by the assistant
					// This processes function calls and matches them with their responses
					functionIDs := make([]string, 0)
					toolCallsResult := messageResult.Get("tool_calls")
					if toolCallsResult.IsArray() {
						parts := make([]client.Part, 0)
						tcsResult := toolCallsResult.Array()

						// Process each tool call in the assistant's message
						for j := 0; j < len(tcsResult); j++ {
							tcResult := tcsResult[j]

							// Extract function call details
							functionID := tcResult.Get("id").String()
							functionIDs = append(functionIDs, functionID)

							functionName := tcResult.Get("function.name").String()
							functionArgs := tcResult.Get("function.arguments").String()

							// Parse function arguments from JSON string to map
							var args map[string]any
							if err := json.Unmarshal([]byte(functionArgs), &args); err == nil {
								parts = append(parts, client.Part{
									FunctionCall: &client.FunctionCall{
										Name: functionName,
										Args: args,
									},
								})
							}
						}

						// Add the model's function calls to the conversation
						if len(parts) > 0 {
							contents = append(contents, client.Content{
								Role: "model", Parts: parts,
							})

							// Create a separate tool response message with the collected responses
							// This matches function calls with their corresponding responses
							toolParts := make([]client.Part, 0)
							for _, functionID := range functionIDs {
								if functionResponse, ok := toolItems[functionID]; ok {
									toolParts = append(toolParts, client.Part{FunctionResponse: functionResponse})
								}
							}
							// Add the tool responses as a separate message in the conversation
							contents = append(contents, client.Content{Role: "tool", Parts: toolParts})
						}
					}
				}
			}
		}
	}

	// Translate the tool declarations from the request.
	var tools []client.ToolDeclaration
	toolsResult := gjson.GetBytes(rawJson, "tools")
	if toolsResult.IsArray() {
		tools = make([]client.ToolDeclaration, 1)
		tools[0].FunctionDeclarations = make([]any, 0)
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolResult := toolsResults[i]
			if toolResult.Get("type").String() == "function" {
				functionTypeResult := toolResult.Get("function")
				if functionTypeResult.Exists() && functionTypeResult.IsObject() {
					var functionDeclaration any
					if err := json.Unmarshal([]byte(functionTypeResult.Raw), &functionDeclaration); err == nil {
						tools[0].FunctionDeclarations = append(tools[0].FunctionDeclarations, functionDeclaration)
					}
				}
			}
		}
	} else {
		tools = make([]client.ToolDeclaration, 0)
	}

	return modelName, systemInstruction, contents, tools
}

// FunctionCallGroup represents a group of function calls and their responses
type FunctionCallGroup struct {
	ModelContent    map[string]interface{}
	FunctionCalls   []gjson.Result
	ResponsesNeeded int
}

// FixCLIToolResponse performs sophisticated tool response format conversion and grouping.
// This function transforms the CLI tool response format by intelligently grouping function calls
// with their corresponding responses, ensuring proper conversation flow and API compatibility.
// It converts from a linear format (1.json) to a grouped format (2.json) where function calls
// and their responses are properly associated and structured.
func FixCLIToolResponse(input string) (string, error) {
	// Parse the input JSON to extract the conversation structure
	parsed := gjson.Parse(input)

	// Extract the contents array which contains the conversation messages
	contents := parsed.Get("request.contents")
	if !contents.Exists() {
		// log.Debugf(input)
		return input, fmt.Errorf("contents not found in input")
	}

	// Initialize data structures for processing and grouping
	var newContents []interface{}          // Final processed contents array
	var pendingGroups []*FunctionCallGroup // Groups awaiting completion with responses
	var collectedResponses []gjson.Result  // Standalone responses to be matched

	// Process each content object in the conversation
	// This iterates through messages and groups function calls with their responses
	contents.ForEach(func(key, value gjson.Result) bool {
		role := value.Get("role").String()
		parts := value.Get("parts")

		// Check if this content has function responses
		var responsePartsInThisContent []gjson.Result
		parts.ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				responsePartsInThisContent = append(responsePartsInThisContent, part)
			}
			return true
		})

		// If this content has function responses, collect them
		if len(responsePartsInThisContent) > 0 {
			collectedResponses = append(collectedResponses, responsePartsInThisContent...)

			// Check if any pending groups can be satisfied
			for i := len(pendingGroups) - 1; i >= 0; i-- {
				group := pendingGroups[i]
				if len(collectedResponses) >= group.ResponsesNeeded {
					// Take the needed responses for this group
					groupResponses := collectedResponses[:group.ResponsesNeeded]
					collectedResponses = collectedResponses[group.ResponsesNeeded:]

					// Create merged function response content
					var responseParts []interface{}
					for _, response := range groupResponses {
						var responseMap map[string]interface{}
						errUnmarshal := json.Unmarshal([]byte(response.Raw), &responseMap)
						if errUnmarshal != nil {
							log.Warnf("failed to unmarshal function response: %v\n", errUnmarshal)
							continue
						}
						responseParts = append(responseParts, responseMap)
					}

					if len(responseParts) > 0 {
						functionResponseContent := map[string]interface{}{
							"parts": responseParts,
							"role":  "function",
						}
						newContents = append(newContents, functionResponseContent)
					}

					// Remove this group as it's been satisfied
					pendingGroups = append(pendingGroups[:i], pendingGroups[i+1:]...)
					break
				}
			}

			return true // Skip adding this content, responses are merged
		}

		// If this is a model with function calls, create a new group
		if role == "model" {
			var functionCallsInThisModel []gjson.Result
			parts.ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					functionCallsInThisModel = append(functionCallsInThisModel, part)
				}
				return true
			})

			if len(functionCallsInThisModel) > 0 {
				// Add the model content
				var contentMap map[string]interface{}
				errUnmarshal := json.Unmarshal([]byte(value.Raw), &contentMap)
				if errUnmarshal != nil {
					log.Warnf("failed to unmarshal model content: %v\n", errUnmarshal)
					return true
				}
				newContents = append(newContents, contentMap)

				// Create a new group for tracking responses
				group := &FunctionCallGroup{
					ModelContent:    contentMap,
					FunctionCalls:   functionCallsInThisModel,
					ResponsesNeeded: len(functionCallsInThisModel),
				}
				pendingGroups = append(pendingGroups, group)
			} else {
				// Regular model content without function calls
				var contentMap map[string]interface{}
				errUnmarshal := json.Unmarshal([]byte(value.Raw), &contentMap)
				if errUnmarshal != nil {
					log.Warnf("failed to unmarshal content: %v\n", errUnmarshal)
					return true
				}
				newContents = append(newContents, contentMap)
			}
		} else {
			// Non-model content (user, etc.)
			var contentMap map[string]interface{}
			errUnmarshal := json.Unmarshal([]byte(value.Raw), &contentMap)
			if errUnmarshal != nil {
				log.Warnf("failed to unmarshal content: %v\n", errUnmarshal)
				return true
			}
			newContents = append(newContents, contentMap)
		}

		return true
	})

	// Handle any remaining pending groups with remaining responses
	for _, group := range pendingGroups {
		if len(collectedResponses) >= group.ResponsesNeeded {
			groupResponses := collectedResponses[:group.ResponsesNeeded]
			collectedResponses = collectedResponses[group.ResponsesNeeded:]

			var responseParts []interface{}
			for _, response := range groupResponses {
				var responseMap map[string]interface{}
				errUnmarshal := json.Unmarshal([]byte(response.Raw), &responseMap)
				if errUnmarshal != nil {
					log.Warnf("failed to unmarshal function response: %v\n", errUnmarshal)
					continue
				}
				responseParts = append(responseParts, responseMap)
			}

			if len(responseParts) > 0 {
				functionResponseContent := map[string]interface{}{
					"parts": responseParts,
					"role":  "function",
				}
				newContents = append(newContents, functionResponseContent)
			}
		}
	}

	// Update the original JSON with the new contents
	result := input
	newContentsJSON, _ := json.Marshal(newContents)
	result, _ = sjson.Set(result, "request.contents", json.RawMessage(newContentsJSON))

	return result, nil
}

func PrepareClaudeRequest(rawJson []byte) (string, *client.Content, []client.Content, []client.ToolDeclaration) {
	var pathsToDelete []string
	root := gjson.ParseBytes(rawJson)
	walk(root, "", "additionalProperties", &pathsToDelete)
	walk(root, "", "$schema", &pathsToDelete)

	var err error
	for _, p := range pathsToDelete {
		rawJson, err = sjson.DeleteBytes(rawJson, p)
		if err != nil {
			continue
		}
	}
	rawJson = bytes.Replace(rawJson, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`), -1)

	// log.Debug(string(rawJson))
	modelName := "gemini-2.5-pro"
	modelResult := gjson.GetBytes(rawJson, "model")
	if modelResult.Type == gjson.String {
		modelName = modelResult.String()
	}

	contents := make([]client.Content, 0)

	var systemInstruction *client.Content

	systemResult := gjson.GetBytes(rawJson, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		systemInstruction = &client.Content{Role: "user", Parts: []client.Part{}}
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == gjson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				systemPart := client.Part{Text: systemPrompt}
				systemInstruction.Parts = append(systemInstruction.Parts, systemPart)
			}
		}
		if len(systemInstruction.Parts) == 0 {
			systemInstruction = nil
		}
	}

	messagesResult := gjson.GetBytes(rawJson, "messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		for i := 0; i < len(messageResults); i++ {
			messageResult := messageResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				continue
			}
			role := roleResult.String()
			if role == "assistant" {
				role = "model"
			}
			clientContent := client.Content{Role: role, Parts: []client.Part{}}

			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentResults := contentsResult.Array()
				for j := 0; j < len(contentResults); j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						clientContent.Parts = append(clientContent.Parts, client.Part{Text: prompt})
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_use" {
						functionName := contentResult.Get("name").String()
						functionArgs := contentResult.Get("input").String()
						var args map[string]any
						if err = json.Unmarshal([]byte(functionArgs), &args); err == nil {
							clientContent.Parts = append(clientContent.Parts, client.Part{
								FunctionCall: &client.FunctionCall{
									Name: functionName,
									Args: args,
								},
							})
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_result" {
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID != "" {
							funcName := toolCallID
							toolCallIDs := strings.Split(toolCallID, "-")
							if len(toolCallIDs) > 1 {
								funcName = strings.Join(toolCallIDs[0:len(toolCallIDs)-1], "-")
							}
							responseData := contentResult.Get("content").String()
							functionResponse := client.FunctionResponse{Name: funcName, Response: map[string]interface{}{"result": responseData}}
							clientContent.Parts = append(clientContent.Parts, client.Part{FunctionResponse: &functionResponse})
						}
					}
				}
				contents = append(contents, clientContent)
			} else if contentsResult.Type == gjson.String {
				prompt := contentsResult.String()
				contents = append(contents, client.Content{Role: role, Parts: []client.Part{{Text: prompt}}})
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
			toolResult := toolsResults[i]
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				inputSchema := inputSchemaResult.Raw
				inputSchema, _ = sjson.Delete(inputSchema, "additionalProperties")
				inputSchema, _ = sjson.Delete(inputSchema, "$schema")

				tool, _ := sjson.Delete(toolResult.Raw, "input_schema")
				tool, _ = sjson.SetRaw(tool, "parameters", inputSchema)
				var toolDeclaration any
				if err = json.Unmarshal([]byte(tool), &toolDeclaration); err == nil {
					tools[0].FunctionDeclarations = append(tools[0].FunctionDeclarations, toolDeclaration)
				}
			}
		}
	} else {
		tools = make([]client.ToolDeclaration, 0)
	}

	return modelName, systemInstruction, contents, tools
}

func walk(value gjson.Result, path, field string, pathsToDelete *[]string) {
	switch value.Type {
	case gjson.JSON:
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			if path == "" {
				childPath = key.String()
			} else {
				childPath = path + "." + key.String()
			}
			if key.String() == field {
				*pathsToDelete = append(*pathsToDelete, childPath)
			}
			walk(val, childPath, field, pathsToDelete)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
	}
}
