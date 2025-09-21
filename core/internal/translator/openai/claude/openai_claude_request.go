// Package claude provides request translation functionality for Anthropic to OpenAI API.
// It handles parsing and transforming Anthropic API requests into OpenAI Chat Completions API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Anthropic API format and OpenAI API's expected format.
package claude

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToOpenAI parses and transforms an Anthropic API request into OpenAI Chat Completions API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	// Base OpenAI Chat Completions API template
	out := `{"model":"","messages":[]}`

	root := gjson.ParseBytes(rawJSON)

	// Model mapping
	out, _ = sjson.Set(out, "model", modelName)

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
	out, _ = sjson.Set(out, "stream", stream)

	// Process messages and system
	var messagesJSON = "[]"

	// Handle system message first
	systemMsgJSON := `{"role":"system","content":[{"type":"text","text":"Use ANY tool, the parameters MUST accord with RFC 8259 (The JavaScript Object Notation (JSON) Data Interchange Format), the keys and value MUST be enclosed in double quotes."}]}`
	if system := root.Get("system"); system.Exists() {
		if system.Type == gjson.String {
			if system.String() != "" {
				oldSystem := `{"type":"text","text":""}`
				oldSystem, _ = sjson.Set(oldSystem, "text", system.String())
				systemMsgJSON, _ = sjson.SetRaw(systemMsgJSON, "content.-1", oldSystem)
			}
		} else if system.Type == gjson.JSON {
			if system.IsArray() {
				systemResults := system.Array()
				for i := 0; i < len(systemResults); i++ {
					systemMsgJSON, _ = sjson.SetRaw(systemMsgJSON, "content.-1", systemResults[i].Raw)
				}
			}
		}
	}
	messagesJSON, _ = sjson.SetRaw(messagesJSON, "-1", systemMsgJSON)

	// Process Anthropic messages
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			// Handle content
			if contentResult.Exists() && contentResult.IsArray() {
				var textParts []string
				var toolCalls []interface{}

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
						toolCallJSON := `{"id":"","type":"function","function":{"name":"","arguments":""}}`
						toolCallJSON, _ = sjson.Set(toolCallJSON, "id", part.Get("id").String())
						toolCallJSON, _ = sjson.Set(toolCallJSON, "function.name", part.Get("name").String())

						// Convert input to arguments JSON string
						if input := part.Get("input"); input.Exists() {
							if inputJSON, err := json.Marshal(input.Value()); err == nil {
								toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", string(inputJSON))
							} else {
								toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", "{}")
							}
						} else {
							toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", "{}")
						}

						toolCalls = append(toolCalls, gjson.Parse(toolCallJSON).Value())

					case "tool_result":
						// Convert to OpenAI tool message format and add immediately to preserve order
						toolResultJSON := `{"role":"tool","tool_call_id":"","content":""}`
						toolResultJSON, _ = sjson.Set(toolResultJSON, "tool_call_id", part.Get("tool_use_id").String())
						toolResultJSON, _ = sjson.Set(toolResultJSON, "content", part.Get("content").String())
						messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(toolResultJSON).Value())
					}
					return true
				})

				// Create main message if there's text content or tool calls
				if len(textParts) > 0 || len(toolCalls) > 0 {
					msgJSON := `{"role":"","content":""}`
					msgJSON, _ = sjson.Set(msgJSON, "role", role)

					// Set content
					if len(textParts) > 0 {
						msgJSON, _ = sjson.Set(msgJSON, "content", strings.Join(textParts, ""))
					} else {
						msgJSON, _ = sjson.Set(msgJSON, "content", "")
					}

					// Set tool calls for assistant messages
					if role == "assistant" && len(toolCalls) > 0 {
						toolCallsJSON, _ := json.Marshal(toolCalls)
						msgJSON, _ = sjson.SetRaw(msgJSON, "tool_calls", string(toolCallsJSON))
					}

					if gjson.Get(msgJSON, "content").String() != "" || len(toolCalls) != 0 {
						messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(msgJSON).Value())
					}
				}

			} else if contentResult.Exists() && contentResult.Type == gjson.String {
				// Simple string content
				msgJSON := `{"role":"","content":""}`
				msgJSON, _ = sjson.Set(msgJSON, "role", role)
				msgJSON, _ = sjson.Set(msgJSON, "content", contentResult.String())
				messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(msgJSON).Value())
			}

			return true
		})
	}

	// Set messages
	if gjson.Parse(messagesJSON).IsArray() && len(gjson.Parse(messagesJSON).Array()) > 0 {
		out, _ = sjson.SetRaw(out, "messages", messagesJSON)
	}

	// Process tools - convert Anthropic tools to OpenAI functions
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var toolsJSON = "[]"

		tools.ForEach(func(_, tool gjson.Result) bool {
			openAIToolJSON := `{"type":"function","function":{"name":"","description":""}}`
			openAIToolJSON, _ = sjson.Set(openAIToolJSON, "function.name", tool.Get("name").String())
			openAIToolJSON, _ = sjson.Set(openAIToolJSON, "function.description", tool.Get("description").String())

			// Convert Anthropic input_schema to OpenAI function parameters
			if inputSchema := tool.Get("input_schema"); inputSchema.Exists() {
				openAIToolJSON, _ = sjson.Set(openAIToolJSON, "function.parameters", inputSchema.Value())
			}

			toolsJSON, _ = sjson.Set(toolsJSON, "-1", gjson.Parse(openAIToolJSON).Value())
			return true
		})

		if gjson.Parse(toolsJSON).IsArray() && len(gjson.Parse(toolsJSON).Array()) > 0 {
			out, _ = sjson.SetRaw(out, "tools", toolsJSON)
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
			toolChoiceJSON := `{"type":"function","function":{"name":""}}`
			toolChoiceJSON, _ = sjson.Set(toolChoiceJSON, "function.name", toolName)
			out, _ = sjson.SetRaw(out, "tool_choice", toolChoiceJSON)
		default:
			// Default to auto if not specified
			out, _ = sjson.Set(out, "tool_choice", "auto")
		}
	}

	// Handle user parameter (for tracking)
	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.Set(out, "user", user.String())
	}

	return []byte(out)
}
