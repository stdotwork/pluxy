// Package openai provides request translation functionality for OpenAI to Anthropic API.
// It handles parsing and transforming OpenAI Chat Completions API requests into Anthropic API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between OpenAI API format and Anthropic API's expected format.
package openai

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIRequestToAnthropic parses and transforms an OpenAI Chat Completions API request into Anthropic API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Anthropic API.
func ConvertOpenAIRequestToAnthropic(rawJSON []byte) string {
	// Base Anthropic API template
	out := `{"model":"","max_tokens":32000,"messages":[]}`

	root := gjson.ParseBytes(rawJSON)

	// Helper for generating tool call IDs in the form: toolu_<alphanum>
	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 24 chars random suffix
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "toolu_" + b.String()
	}

	// Model mapping
	if model := root.Get("model"); model.Exists() {
		modelStr := model.String()
		out, _ = sjson.Set(out, "model", modelStr)
	}

	// Max tokens
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	}

	// Temperature
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.Set(out, "temperature", temp.Float())
	}

	// Top P
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.Set(out, "top_p", topP.Float())
	}

	// Stop sequences
	if stop := root.Get("stop"); stop.Exists() {
		if stop.IsArray() {
			var stopSequences []string
			stop.ForEach(func(_, value gjson.Result) bool {
				stopSequences = append(stopSequences, value.String())
				return true
			})
			if len(stopSequences) > 0 {
				out, _ = sjson.Set(out, "stop_sequences", stopSequences)
			}
		} else {
			out, _ = sjson.Set(out, "stop_sequences", []string{stop.String()})
		}
	}

	// Stream
	if stream := root.Get("stream"); stream.Exists() {
		out, _ = sjson.Set(out, "stream", stream.Bool())
	}

	// Process messages
	var anthropicMessages []interface{}
	var toolCallIDs []string // Track tool call IDs for matching with tool results

	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			switch role {
			case "system", "user", "assistant":
				// Create Anthropic message
				if role == "system" {
					role = "user"
				}

				msg := map[string]interface{}{
					"role":    role,
					"content": []interface{}{},
				}

				// Handle content
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					// Simple text content
					msg["content"] = []interface{}{
						map[string]interface{}{
							"type": "text",
							"text": contentResult.String(),
						},
					}
				} else if contentResult.Exists() && contentResult.IsArray() {
					// Array of content parts
					var contentParts []interface{}
					contentResult.ForEach(func(_, part gjson.Result) bool {
						partType := part.Get("type").String()

						switch partType {
						case "text":
							contentParts = append(contentParts, map[string]interface{}{
								"type": "text",
								"text": part.Get("text").String(),
							})

						case "image_url":
							// Convert OpenAI image format to Anthropic format
							imageURL := part.Get("image_url.url").String()
							if strings.HasPrefix(imageURL, "data:") {
								// Extract base64 data and media type
								parts := strings.Split(imageURL, ",")
								if len(parts) == 2 {
									mediaTypePart := strings.Split(parts[0], ";")[0]
									mediaType := strings.TrimPrefix(mediaTypePart, "data:")
									data := parts[1]

									contentParts = append(contentParts, map[string]interface{}{
										"type": "image",
										"source": map[string]interface{}{
											"type":       "base64",
											"media_type": mediaType,
											"data":       data,
										},
									})
								}
							}
						}
						return true
					})
					if len(contentParts) > 0 {
						msg["content"] = contentParts
					}
				} else {
					// Initialize empty content array for tool calls
					msg["content"] = []interface{}{}
				}

				// Handle tool calls (for assistant messages)
				if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && role == "assistant" {
					var contentParts []interface{}

					// Add existing text content if any
					if existingContent, ok := msg["content"].([]interface{}); ok {
						contentParts = existingContent
					}

					toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
						if toolCall.Get("type").String() == "function" {
							toolCallID := toolCall.Get("id").String()
							if toolCallID == "" {
								toolCallID = genToolCallID()
							}
							toolCallIDs = append(toolCallIDs, toolCallID)

							function := toolCall.Get("function")
							toolUse := map[string]interface{}{
								"type": "tool_use",
								"id":   toolCallID,
								"name": function.Get("name").String(),
							}

							// Parse arguments
							if args := function.Get("arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" {
									var argsMap map[string]interface{}
									if err := json.Unmarshal([]byte(argsStr), &argsMap); err == nil {
										toolUse["input"] = argsMap
									} else {
										toolUse["input"] = map[string]interface{}{}
									}
								} else {
									toolUse["input"] = map[string]interface{}{}
								}
							} else {
								toolUse["input"] = map[string]interface{}{}
							}

							contentParts = append(contentParts, toolUse)
						}
						return true
					})
					msg["content"] = contentParts
				}

				anthropicMessages = append(anthropicMessages, msg)

			case "tool":
				// Handle tool result messages
				toolCallID := message.Get("tool_call_id").String()
				content := message.Get("content").String()

				// Create tool result message
				msg := map[string]interface{}{
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{
							"type":        "tool_result",
							"tool_use_id": toolCallID,
							"content":     content,
						},
					},
				}

				anthropicMessages = append(anthropicMessages, msg)
			}
			return true
		})
	}

	// Set messages
	if len(anthropicMessages) > 0 {
		messagesJSON, _ := json.Marshal(anthropicMessages)
		out, _ = sjson.SetRaw(out, "messages", string(messagesJSON))
	}

	// Tools mapping: OpenAI tools -> Anthropic tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var anthropicTools []interface{}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				function := tool.Get("function")
				anthropicTool := map[string]interface{}{
					"name":        function.Get("name").String(),
					"description": function.Get("description").String(),
				}

				// Convert parameters schema
				if parameters := function.Get("parameters"); parameters.Exists() {
					anthropicTool["input_schema"] = parameters.Value()
				}

				anthropicTools = append(anthropicTools, anthropicTool)
			}
			return true
		})

		if len(anthropicTools) > 0 {
			toolsJSON, _ := json.Marshal(anthropicTools)
			out, _ = sjson.SetRaw(out, "tools", string(toolsJSON))
		}
	}

	// Tool choice mapping
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			choice := toolChoice.String()
			switch choice {
			case "none":
				// Don't set tool_choice, Anthropic will not use tools
			case "auto":
				out, _ = sjson.Set(out, "tool_choice", map[string]interface{}{"type": "auto"})
			case "required":
				out, _ = sjson.Set(out, "tool_choice", map[string]interface{}{"type": "any"})
			}
		case gjson.JSON:
			// Specific tool choice
			if toolChoice.Get("type").String() == "function" {
				functionName := toolChoice.Get("function.name").String()
				out, _ = sjson.Set(out, "tool_choice", map[string]interface{}{
					"type": "tool",
					"name": functionName,
				})
			}
		default:
		}
	}

	return out
}
