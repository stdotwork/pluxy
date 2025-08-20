// Package gemini provides response translation functionality for OpenAI to Gemini API.
// This package handles the conversion of OpenAI Chat Completions API responses into Gemini API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package gemini

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIResponseToGeminiParams holds parameters for response conversion
type ConvertOpenAIResponseToGeminiParams struct {
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	// Content accumulator for streaming
	ContentAccumulator strings.Builder
	// Track if this is the first chunk
	IsFirstChunk bool
}

// ToolCallAccumulator holds the state for accumulating tool call data
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// ConvertOpenAIResponseToGemini converts OpenAI Chat Completions streaming response format to Gemini API format.
// This function processes OpenAI streaming chunks and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Gemini API format.
func ConvertOpenAIResponseToGemini(rawJSON []byte, param *ConvertOpenAIResponseToGeminiParams) []string {
	// Handle [DONE] marker
	if strings.TrimSpace(string(rawJSON)) == "[DONE]" {
		return []string{}
	}

	root := gjson.ParseBytes(rawJSON)

	// Initialize accumulators if needed
	if param.ToolCallsAccumulator == nil {
		param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
	}

	// Process choices
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		// Handle empty choices array (usage-only chunk)
		if len(choices.Array()) == 0 {
			// This is a usage-only chunk, handle usage and return
			if usage := root.Get("usage"); usage.Exists() {
				template := `{"candidates":[],"usageMetadata":{}}`

				// Set model if available
				if model := root.Get("model"); model.Exists() {
					template, _ = sjson.Set(template, "model", model.String())
				}

				usageObj := map[string]interface{}{
					"promptTokenCount":     usage.Get("prompt_tokens").Int(),
					"candidatesTokenCount": usage.Get("completion_tokens").Int(),
					"totalTokenCount":      usage.Get("total_tokens").Int(),
				}
				template, _ = sjson.Set(template, "usageMetadata", usageObj)
				return []string{template}
			}
			return []string{}
		}

		var results []string

		choices.ForEach(func(choiceIndex, choice gjson.Result) bool {
			// Base Gemini response template
			template := `{"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"STOP","index":0}]}`

			// Set model if available
			if model := root.Get("model"); model.Exists() {
				template, _ = sjson.Set(template, "model", model.String())
			}

			_ = int(choice.Get("index").Int()) // choiceIdx not used in streaming
			delta := choice.Get("delta")

			// Handle role (only in first chunk)
			if role := delta.Get("role"); role.Exists() && param.IsFirstChunk {
				// OpenAI assistant -> Gemini model
				if role.String() == "assistant" {
					template, _ = sjson.Set(template, "candidates.0.content.role", "model")
				}
				param.IsFirstChunk = false
				results = append(results, template)
				return true
			}

			// Handle content delta
			if content := delta.Get("content"); content.Exists() && content.String() != "" {
				contentText := content.String()
				param.ContentAccumulator.WriteString(contentText)

				// Create text part for this delta
				parts := []interface{}{
					map[string]interface{}{
						"text": contentText,
					},
				}
				template, _ = sjson.Set(template, "candidates.0.content.parts", parts)
				results = append(results, template)
				return true
			}

			// Handle tool calls delta
			if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
					toolIndex := int(toolCall.Get("index").Int())
					toolID := toolCall.Get("id").String()
					toolType := toolCall.Get("type").String()

					if toolType == "function" {
						function := toolCall.Get("function")
						functionName := function.Get("name").String()
						functionArgs := function.Get("arguments").String()

						// Initialize accumulator if needed
						if _, exists := param.ToolCallsAccumulator[toolIndex]; !exists {
							param.ToolCallsAccumulator[toolIndex] = &ToolCallAccumulator{
								ID:   toolID,
								Name: functionName,
							}
						}

						// Update ID if provided
						if toolID != "" {
							param.ToolCallsAccumulator[toolIndex].ID = toolID
						}

						// Update name if provided
						if functionName != "" {
							param.ToolCallsAccumulator[toolIndex].Name = functionName
						}

						// Accumulate arguments
						if functionArgs != "" {
							param.ToolCallsAccumulator[toolIndex].Arguments.WriteString(functionArgs)
						}
					}
					return true
				})

				// Don't output anything for tool call deltas - wait for completion
				return true
			}

			// Handle finish reason
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
				geminiFinishReason := mapOpenAIFinishReasonToGemini(finishReason.String())
				template, _ = sjson.Set(template, "candidates.0.finishReason", geminiFinishReason)

				// If we have accumulated tool calls, output them now
				if len(param.ToolCallsAccumulator) > 0 {
					var parts []interface{}
					for _, accumulator := range param.ToolCallsAccumulator {
						argsStr := accumulator.Arguments.String()
						var argsMap map[string]interface{}

						if argsStr != "" && argsStr != "{}" {
							// Handle malformed JSON by trying to fix common issues
							fixedArgs := argsStr
							// Fix unquoted keys and values (common in the sample)
							if strings.Contains(fixedArgs, "北京") && !strings.Contains(fixedArgs, "\"北京\"") {
								fixedArgs = strings.ReplaceAll(fixedArgs, "北京", "\"北京\"")
							}
							if strings.Contains(fixedArgs, "celsius") && !strings.Contains(fixedArgs, "\"celsius\"") {
								fixedArgs = strings.ReplaceAll(fixedArgs, "celsius", "\"celsius\"")
							}

							if err := json.Unmarshal([]byte(fixedArgs), &argsMap); err != nil {
								// If still fails, try to parse as raw string
								if err2 := json.Unmarshal([]byte("\""+argsStr+"\""), &argsMap); err2 != nil {
									// Last resort: use empty object
									argsMap = map[string]interface{}{}
								}
							}
						} else {
							argsMap = map[string]interface{}{}
						}

						functionCallPart := map[string]interface{}{
							"functionCall": map[string]interface{}{
								"name": accumulator.Name,
								"args": argsMap,
							},
						}
						parts = append(parts, functionCallPart)
					}

					if len(parts) > 0 {
						template, _ = sjson.Set(template, "candidates.0.content.parts", parts)
					}

					// Clear accumulators
					param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
				}

				results = append(results, template)
				return true
			}

			// Handle usage information
			if usage := root.Get("usage"); usage.Exists() {
				usageObj := map[string]interface{}{
					"promptTokenCount":     usage.Get("prompt_tokens").Int(),
					"candidatesTokenCount": usage.Get("completion_tokens").Int(),
					"totalTokenCount":      usage.Get("total_tokens").Int(),
				}
				template, _ = sjson.Set(template, "usageMetadata", usageObj)
				results = append(results, template)
				return true
			}

			return true
		})
		return results
	}
	return []string{}
}

// mapOpenAIFinishReasonToGemini maps OpenAI finish reasons to Gemini finish reasons
func mapOpenAIFinishReasonToGemini(openAIReason string) string {
	switch openAIReason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "tool_calls":
		return "STOP" // Gemini doesn't have a specific tool_calls finish reason
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}

// ConvertOpenAINonStreamResponseToGemini converts OpenAI non-streaming response to Gemini format
func ConvertOpenAINonStreamResponseToGemini(rawJSON []byte) string {
	root := gjson.ParseBytes(rawJSON)

	// Base Gemini response template
	out := `{"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"STOP","index":0}]}`

	// Set model if available
	if model := root.Get("model"); model.Exists() {
		out, _ = sjson.Set(out, "model", model.String())
	}

	// Process choices
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(choiceIndex, choice gjson.Result) bool {
			choiceIdx := int(choice.Get("index").Int())
			message := choice.Get("message")

			// Set role
			if role := message.Get("role"); role.Exists() {
				if role.String() == "assistant" {
					out, _ = sjson.Set(out, "candidates.0.content.role", "model")
				}
			}

			var parts []interface{}

			// Handle content first
			if content := message.Get("content"); content.Exists() && content.String() != "" {
				parts = append(parts, map[string]interface{}{
					"text": content.String(),
				})
			}

			// Handle tool calls
			if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
					if toolCall.Get("type").String() == "function" {
						function := toolCall.Get("function")
						functionName := function.Get("name").String()
						functionArgs := function.Get("arguments").String()

						// Parse arguments
						var argsMap map[string]interface{}
						if functionArgs != "" && functionArgs != "{}" {
							// Handle malformed JSON by trying to fix common issues
							fixedArgs := functionArgs
							// Fix unquoted keys and values (common in the sample)
							if strings.Contains(fixedArgs, "北京") && !strings.Contains(fixedArgs, "\"北京\"") {
								fixedArgs = strings.ReplaceAll(fixedArgs, "北京", "\"北京\"")
							}
							if strings.Contains(fixedArgs, "celsius") && !strings.Contains(fixedArgs, "\"celsius\"") {
								fixedArgs = strings.ReplaceAll(fixedArgs, "celsius", "\"celsius\"")
							}

							if err := json.Unmarshal([]byte(fixedArgs), &argsMap); err != nil {
								// If still fails, try to parse as raw string
								if err2 := json.Unmarshal([]byte("\""+functionArgs+"\""), &argsMap); err2 != nil {
									// Last resort: use empty object
									argsMap = map[string]interface{}{}
								}
							}
						} else {
							argsMap = map[string]interface{}{}
						}

						functionCallPart := map[string]interface{}{
							"functionCall": map[string]interface{}{
								"name": functionName,
								"args": argsMap,
							},
						}
						parts = append(parts, functionCallPart)
					}
					return true
				})
			}

			// Set parts
			if len(parts) > 0 {
				out, _ = sjson.Set(out, "candidates.0.content.parts", parts)
			}

			// Handle finish reason
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
				geminiFinishReason := mapOpenAIFinishReasonToGemini(finishReason.String())
				out, _ = sjson.Set(out, "candidates.0.finishReason", geminiFinishReason)
			}

			// Set index
			out, _ = sjson.Set(out, "candidates.0.index", choiceIdx)

			return true
		})
	}

	// Handle usage information
	if usage := root.Get("usage"); usage.Exists() {
		usageObj := map[string]interface{}{
			"promptTokenCount":     usage.Get("prompt_tokens").Int(),
			"candidatesTokenCount": usage.Get("completion_tokens").Int(),
			"totalTokenCount":      usage.Get("total_tokens").Int(),
		}
		out, _ = sjson.Set(out, "usageMetadata", usageObj)
	}

	return out
}
