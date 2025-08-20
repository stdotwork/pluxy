// Package claude provides request translation functionality for Anthropic to OpenAI API.
// It handles parsing and transforming Anthropic API requests into OpenAI Chat Completions API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Anthropic API format and OpenAI API's expected format.
package claude

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertAnthropicRequestToOpenAI parses and transforms an Anthropic API request into OpenAI Chat Completions API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertAnthropicRequestToOpenAI(rawJSON []byte) string {
	// Base OpenAI Chat Completions API template
	out := `{"model":"","messages":[]}`

	root := gjson.ParseBytes(rawJSON)

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

	// Stop sequences -> stop
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() {
		if stopSequences.IsArray() {
			var stops []string
			stopSequences.ForEach(func(_, value gjson.Result) bool {
				stops = append(stops, value.String())
				return true
			})
			if len(stops) > 0 {
				if len(stops) == 1 {
					out, _ = sjson.Set(out, "stop", stops[0])
				} else {
					out, _ = sjson.Set(out, "stop", stops)
				}
			}
		}
	}

	// Stream
	if stream := root.Get("stream"); stream.Exists() {
		out, _ = sjson.Set(out, "stream", stream.Bool())
	}

	// Process messages and system
	var openAIMessages []interface{}

	// Handle system message first
	if system := root.Get("system"); system.Exists() && system.String() != "" {
		systemMsg := map[string]interface{}{
			"role":    "system",
			"content": system.String(),
		}
		openAIMessages = append(openAIMessages, systemMsg)
	}

	// Process Anthropic messages
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			msg := map[string]interface{}{
				"role": role,
			}

			// Handle content
			if contentResult.Exists() && contentResult.IsArray() {
				var textParts []string
				var toolCalls []interface{}
				var toolResults []interface{}

				contentResult.ForEach(func(_, part gjson.Result) bool {
					partType := part.Get("type").String()

					switch partType {
					case "text":
						textParts = append(textParts, part.Get("text").String())

					case "image":
						// Convert Anthropic image format to OpenAI format
						if source := part.Get("source"); source.Exists() {
							sourceType := source.Get("type").String()
							if sourceType == "base64" {
								mediaType := source.Get("media_type").String()
								data := source.Get("data").String()
								imageURL := "data:" + mediaType + ";base64," + data

								// For now, add as text since OpenAI image handling is complex
								// In a real implementation, you'd need to handle this properly
								textParts = append(textParts, "[Image: "+imageURL+"]")
							}
						}

					case "tool_use":
						// Convert to OpenAI tool call format
						toolCall := map[string]interface{}{
							"id":   part.Get("id").String(),
							"type": "function",
							"function": map[string]interface{}{
								"name": part.Get("name").String(),
							},
						}

						// Convert input to arguments JSON string
						if input := part.Get("input"); input.Exists() {
							if inputJSON, err := json.Marshal(input.Value()); err == nil {
								if function, ok := toolCall["function"].(map[string]interface{}); ok {
									function["arguments"] = string(inputJSON)
								}
							} else {
								if function, ok := toolCall["function"].(map[string]interface{}); ok {
									function["arguments"] = "{}"
								}
							}
						} else {
							if function, ok := toolCall["function"].(map[string]interface{}); ok {
								function["arguments"] = "{}"
							}
						}

						toolCalls = append(toolCalls, toolCall)

					case "tool_result":
						// Convert to OpenAI tool message format
						toolResult := map[string]interface{}{
							"role":         "tool",
							"tool_call_id": part.Get("tool_use_id").String(),
							"content":      part.Get("content").String(),
						}
						toolResults = append(toolResults, toolResult)
					}
					return true
				})

				// Set content
				if len(textParts) > 0 {
					msg["content"] = strings.Join(textParts, "")
				} else {
					msg["content"] = ""
				}

				// Set tool calls for assistant messages
				if role == "assistant" && len(toolCalls) > 0 {
					msg["tool_calls"] = toolCalls
				}

				openAIMessages = append(openAIMessages, msg)

				// Add tool result messages separately
				for _, toolResult := range toolResults {
					openAIMessages = append(openAIMessages, toolResult)
				}

			} else if contentResult.Exists() && contentResult.Type == gjson.String {
				// Simple string content
				msg["content"] = contentResult.String()
				openAIMessages = append(openAIMessages, msg)
			}

			return true
		})
	}

	// Set messages
	if len(openAIMessages) > 0 {
		messagesJSON, _ := json.Marshal(openAIMessages)
		out, _ = sjson.SetRaw(out, "messages", string(messagesJSON))
	}

	// Process tools - convert Anthropic tools to OpenAI functions
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var openAITools []interface{}

		tools.ForEach(func(_, tool gjson.Result) bool {
			openAITool := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        tool.Get("name").String(),
					"description": tool.Get("description").String(),
				},
			}

			// Convert Anthropic input_schema to OpenAI function parameters
			if inputSchema := tool.Get("input_schema"); inputSchema.Exists() {
				if function, ok := openAITool["function"].(map[string]interface{}); ok {
					function["parameters"] = inputSchema.Value()
				}
			}

			openAITools = append(openAITools, openAITool)
			return true
		})

		if len(openAITools) > 0 {
			toolsJSON, _ := json.Marshal(openAITools)
			out, _ = sjson.SetRaw(out, "tools", string(toolsJSON))
		}
	}

	// Tool choice mapping - convert Anthropic tool_choice to OpenAI format
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Get("type").String() {
		case "auto":
			out, _ = sjson.Set(out, "tool_choice", "auto")
		case "any":
			out, _ = sjson.Set(out, "tool_choice", "required")
		case "tool":
			// Specific tool choice
			toolName := toolChoice.Get("name").String()
			out, _ = sjson.Set(out, "tool_choice", map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": toolName,
				},
			})
		default:
			// Default to auto if not specified
			out, _ = sjson.Set(out, "tool_choice", "auto")
		}
	}

	// Handle user parameter (for tracking)
	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.Set(out, "user", user.String())
	}

	return out
}
