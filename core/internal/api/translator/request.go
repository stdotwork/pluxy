package translator

import (
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

	// Process the array of messages.
	contents := make([]client.Content, 0)
	var systemInstruction *client.Content
	messagesResult := gjson.GetBytes(rawJson, "messages")

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
			if roleResult.String() == "tool" {
				toolCallID := messageResult.Get("tool_call_id").String()
				if toolCallID != "" {
					var responseData string
					if contentResult.Type == gjson.String {
						responseData = contentResult.String()
					} else if contentResult.IsObject() && contentResult.Get("type").String() == "text" {
						responseData = contentResult.Get("text").String()
					}

					// drop the timestamp from the tool call ID
					toolCallIDs := strings.Split(toolCallID, "-")
					strings.Join(toolCallIDs, "-")
					newToolCallID := strings.Join(toolCallIDs[:len(toolCallIDs)-1], "-")

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
			// Assistant messages can contain text or tool calls.
			case "assistant":
				if contentResult.Type == gjson.String {
					contents = append(contents, client.Content{Role: "model", Parts: []client.Part{{Text: contentResult.String()}}})
				} else if !contentResult.Exists() || contentResult.Type == gjson.Null {
					// Handle tool calls made by the assistant.
					functionIDs := make([]string, 0)
					toolCallsResult := messageResult.Get("tool_calls")
					if toolCallsResult.IsArray() {
						parts := make([]client.Part, 0)
						tcsResult := toolCallsResult.Array()
						for j := 0; j < len(tcsResult); j++ {
							tcResult := tcsResult[j]

							functionID := tcResult.Get("id").String()
							functionIDs = append(functionIDs, functionID)

							functionName := tcResult.Get("function.name").String()
							functionArgs := tcResult.Get("function.arguments").String()
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
						if len(parts) > 0 {
							contents = append(contents, client.Content{
								Role: "model", Parts: parts,
							})

							toolParts := make([]client.Part, 0)
							for _, functionID := range functionIDs {
								if functionResponse, ok := toolItems[functionID]; ok {
									toolParts = append(toolParts, client.Part{FunctionResponse: functionResponse})
								}
							}
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

// FixCLIToolResponse converts the format from 1.json to 2.json
// It groups function calls with their corresponding responses
func FixCLIToolResponse(input string) (string, error) {
	// Parse the input JSON
	parsed := gjson.Parse(input)

	// Get the contents array
	contents := parsed.Get("request.contents")
	if !contents.Exists() {
		return input, fmt.Errorf("contents not found in input")
	}

	var newContents []interface{}
	var pendingGroups []*FunctionCallGroup
	var collectedResponses []gjson.Result

	// Process each content object
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
