// Package code provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package code

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeCodeRequestToCli parses and transforms a Claude API request into internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
func ConvertClaudeCodeRequestToCli(rawJSON []byte) (string, *client.Content, []client.Content, []client.ToolDeclaration) {
	var pathsToDelete []string
	root := gjson.ParseBytes(rawJSON)
	walk(root, "", "additionalProperties", &pathsToDelete)
	walk(root, "", "$schema", &pathsToDelete)

	var err error
	for _, p := range pathsToDelete {
		rawJSON, err = sjson.DeleteBytes(rawJSON, p)
		if err != nil {
			continue
		}
	}
	rawJSON = bytes.Replace(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`), -1)

	// log.Debug(string(rawJSON))
	modelName := "gemini-2.5-pro"
	modelResult := gjson.GetBytes(rawJSON, "model")
	if modelResult.Type == gjson.String {
		modelName = modelResult.String()
	}

	contents := make([]client.Content, 0)

	var systemInstruction *client.Content

	systemResult := gjson.GetBytes(rawJSON, "system")
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

	messagesResult := gjson.GetBytes(rawJSON, "messages")
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
	toolsResult := gjson.GetBytes(rawJSON, "tools")
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
