// Package openai provides response translation functionality for Anthropic to OpenAI API.
// This package handles the conversion of Anthropic API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package openai

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertAnthropicResponseToOpenAIParams holds parameters for response conversion
type ConvertAnthropicResponseToOpenAIParams struct {
	CreatedAt    int64
	ResponseID   string
	FinishReason string
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
}

// ToolCallAccumulator holds the state for accumulating tool call data
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// ConvertAnthropicResponseToOpenAI converts Anthropic streaming response format to OpenAI Chat Completions format.
// This function processes various Anthropic event types and transforms them into OpenAI-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the OpenAI API format.
func ConvertAnthropicResponseToOpenAI(rawJSON []byte, param *ConvertAnthropicResponseToOpenAIParams) []string {
	root := gjson.ParseBytes(rawJSON)
	eventType := root.Get("type").String()

	// Base OpenAI streaming response template
	template := `{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`

	// Set model
	modelResult := gjson.GetBytes(rawJSON, "model")
	modelName := modelResult.String()
	if modelName != "" {
		template, _ = sjson.Set(template, "model", modelName)
	}

	// Set response ID and creation time
	if param.ResponseID != "" {
		template, _ = sjson.Set(template, "id", param.ResponseID)
	}
	if param.CreatedAt > 0 {
		template, _ = sjson.Set(template, "created", param.CreatedAt)
	}

	switch eventType {
	case "message_start":
		// Initialize response with message metadata
		if message := root.Get("message"); message.Exists() {
			param.ResponseID = message.Get("id").String()
			param.CreatedAt = time.Now().Unix()

			template, _ = sjson.Set(template, "id", param.ResponseID)
			template, _ = sjson.Set(template, "model", modelName)
			template, _ = sjson.Set(template, "created", param.CreatedAt)

			// Set initial role
			template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")

			// Initialize tool calls accumulator
			if param.ToolCallsAccumulator == nil {
				param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}
		}
		return []string{template}

	case "content_block_start":
		// Start of a content block
		if contentBlock := root.Get("content_block"); contentBlock.Exists() {
			blockType := contentBlock.Get("type").String()

			if blockType == "tool_use" {
				// Start of tool call - initialize accumulator
				toolCallID := contentBlock.Get("id").String()
				toolName := contentBlock.Get("name").String()
				index := int(root.Get("index").Int())

				if param.ToolCallsAccumulator == nil {
					param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
				}

				param.ToolCallsAccumulator[index] = &ToolCallAccumulator{
					ID:   toolCallID,
					Name: toolName,
				}

				// Don't output anything yet - wait for complete tool call
				return []string{}
			}
		}
		return []string{template}

	case "content_block_delta":
		// Handle content delta (text or tool use)
		if delta := root.Get("delta"); delta.Exists() {
			deltaType := delta.Get("type").String()

			switch deltaType {
			case "text_delta":
				// Text content delta
				if text := delta.Get("text"); text.Exists() {
					template, _ = sjson.Set(template, "choices.0.delta.content", text.String())
				}

			case "input_json_delta":
				// Tool use input delta - accumulate arguments
				if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
					index := int(root.Get("index").Int())
					if param.ToolCallsAccumulator != nil {
						if accumulator, exists := param.ToolCallsAccumulator[index]; exists {
							accumulator.Arguments.WriteString(partialJSON.String())
						}
					}
				}
				// Don't output anything yet - wait for complete tool call
				return []string{}
			}
		}
		return []string{template}

	case "content_block_stop":
		// End of content block - output complete tool call if it's a tool_use block
		index := int(root.Get("index").Int())
		if param.ToolCallsAccumulator != nil {
			if accumulator, exists := param.ToolCallsAccumulator[index]; exists {
				// Build complete tool call
				arguments := accumulator.Arguments.String()
				if arguments == "" {
					arguments = "{}"
				}

				toolCall := map[string]interface{}{
					"index": index,
					"id":    accumulator.ID,
					"type":  "function",
					"function": map[string]interface{}{
						"name":      accumulator.Name,
						"arguments": arguments,
					},
				}

				template, _ = sjson.Set(template, "choices.0.delta.tool_calls", []interface{}{toolCall})

				// Clean up the accumulator for this index
				delete(param.ToolCallsAccumulator, index)

				return []string{template}
			}
		}
		return []string{}

	case "message_delta":
		// Handle message-level changes
		if delta := root.Get("delta"); delta.Exists() {
			if stopReason := delta.Get("stop_reason"); stopReason.Exists() {
				param.FinishReason = mapAnthropicStopReasonToOpenAI(stopReason.String())
				template, _ = sjson.Set(template, "choices.0.finish_reason", param.FinishReason)
			}
		}

		// Handle usage information
		if usage := root.Get("usage"); usage.Exists() {
			usageObj := map[string]interface{}{
				"prompt_tokens":     usage.Get("input_tokens").Int(),
				"completion_tokens": usage.Get("output_tokens").Int(),
				"total_tokens":      usage.Get("input_tokens").Int() + usage.Get("output_tokens").Int(),
			}
			template, _ = sjson.Set(template, "usage", usageObj)
		}
		return []string{template}

	case "message_stop":
		// Final message - send [DONE]
		return []string{"[DONE]\n"}

	case "ping":
		// Ping events - ignore
		return []string{}

	case "error":
		// Error event
		if errorData := root.Get("error"); errorData.Exists() {
			errorResponse := map[string]interface{}{
				"error": map[string]interface{}{
					"message": errorData.Get("message").String(),
					"type":    errorData.Get("type").String(),
				},
			}
			errorJSON, _ := json.Marshal(errorResponse)
			return []string{string(errorJSON)}
		}
		return []string{}

	default:
		// Unknown event type - ignore
		return []string{}
	}
}

// mapAnthropicStopReasonToOpenAI maps Anthropic stop reasons to OpenAI stop reasons
func mapAnthropicStopReasonToOpenAI(anthropicReason string) string {
	switch anthropicReason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

// ConvertAnthropicStreamingResponseToOpenAINonStream aggregates streaming chunks into a single non-streaming response
// following OpenAI Chat Completions API format with reasoning content support
func ConvertAnthropicStreamingResponseToOpenAINonStream(chunks [][]byte) string {
	// Base OpenAI non-streaming response template
	out := `{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`

	var messageID string
	var model string
	var createdAt int64
	var inputTokens, outputTokens int64
	var reasoningTokens int64
	var stopReason string
	var contentParts []string
	var reasoningParts []string
	// Use map to track tool calls by index for proper merging
	toolCallsMap := make(map[int]map[string]interface{})
	// Track tool call arguments accumulation
	toolCallArgsMap := make(map[int]strings.Builder)

	for _, chunk := range chunks {
		root := gjson.ParseBytes(chunk)
		eventType := root.Get("type").String()

		switch eventType {
		case "message_start":
			if message := root.Get("message"); message.Exists() {
				messageID = message.Get("id").String()
				model = message.Get("model").String()
				createdAt = time.Now().Unix()
				if usage := message.Get("usage"); usage.Exists() {
					inputTokens = usage.Get("input_tokens").Int()
				}
			}

		case "content_block_start":
			// Handle different content block types
			if contentBlock := root.Get("content_block"); contentBlock.Exists() {
				blockType := contentBlock.Get("type").String()
				if blockType == "thinking" {
					// Start of thinking/reasoning content
					continue
				} else if blockType == "tool_use" {
					// Initialize tool call tracking
					index := int(root.Get("index").Int())
					toolCallsMap[index] = map[string]interface{}{
						"id":   contentBlock.Get("id").String(),
						"type": "function",
						"function": map[string]interface{}{
							"name":      contentBlock.Get("name").String(),
							"arguments": "",
						},
					}
					// Initialize arguments builder for this tool call
					toolCallArgsMap[index] = strings.Builder{}
				}
			}

		case "content_block_delta":
			if delta := root.Get("delta"); delta.Exists() {
				deltaType := delta.Get("type").String()
				switch deltaType {
				case "text_delta":
					if text := delta.Get("text"); text.Exists() {
						contentParts = append(contentParts, text.String())
					}
				case "thinking_delta":
					// Anthropic thinking content -> OpenAI reasoning content
					if thinking := delta.Get("thinking"); thinking.Exists() {
						reasoningParts = append(reasoningParts, thinking.String())
					}
				case "input_json_delta":
					// Accumulate tool call arguments
					if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
						index := int(root.Get("index").Int())
						if builder, exists := toolCallArgsMap[index]; exists {
							builder.WriteString(partialJSON.String())
							toolCallArgsMap[index] = builder
						}
					}
				}
			}

		case "content_block_stop":
			// Finalize tool call arguments for this index
			index := int(root.Get("index").Int())
			if toolCall, exists := toolCallsMap[index]; exists {
				if builder, argsExists := toolCallArgsMap[index]; argsExists {
					// Set the accumulated arguments
					arguments := builder.String()
					if arguments == "" {
						arguments = "{}"
					}
					toolCall["function"].(map[string]interface{})["arguments"] = arguments
				}
			}

		case "message_delta":
			if delta := root.Get("delta"); delta.Exists() {
				if sr := delta.Get("stop_reason"); sr.Exists() {
					stopReason = sr.String()
				}
			}
			if usage := root.Get("usage"); usage.Exists() {
				outputTokens = usage.Get("output_tokens").Int()
				// Estimate reasoning tokens from thinking content
				if len(reasoningParts) > 0 {
					reasoningTokens = int64(len(strings.Join(reasoningParts, "")) / 4) // Rough estimation
				}
			}
		}
	}

	// Set basic response fields
	out, _ = sjson.Set(out, "id", messageID)
	out, _ = sjson.Set(out, "created", createdAt)
	out, _ = sjson.Set(out, "model", model)

	// Set message content
	messageContent := strings.Join(contentParts, "")
	out, _ = sjson.Set(out, "choices.0.message.content", messageContent)

	// Add reasoning content if available (following OpenAI reasoning format)
	if len(reasoningParts) > 0 {
		reasoningContent := strings.Join(reasoningParts, "")
		// Add reasoning as a separate field in the message
		out, _ = sjson.Set(out, "choices.0.message.reasoning", reasoningContent)
	}

	// Set tool calls if any
	if len(toolCallsMap) > 0 {
		// Convert tool calls map to array, preserving order by index
		var toolCallsArray []interface{}
		// Find the maximum index to determine the range
		maxIndex := -1
		for index := range toolCallsMap {
			if index > maxIndex {
				maxIndex = index
			}
		}
		// Iterate through all possible indices up to maxIndex
		for i := 0; i <= maxIndex; i++ {
			if toolCall, exists := toolCallsMap[i]; exists {
				toolCallsArray = append(toolCallsArray, toolCall)
			}
		}
		if len(toolCallsArray) > 0 {
			out, _ = sjson.Set(out, "choices.0.message.tool_calls", toolCallsArray)
			out, _ = sjson.Set(out, "choices.0.finish_reason", "tool_calls")
		} else {
			out, _ = sjson.Set(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
		}
	} else {
		out, _ = sjson.Set(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
	}

	// Set usage information
	totalTokens := inputTokens + outputTokens
	out, _ = sjson.Set(out, "usage.prompt_tokens", inputTokens)
	out, _ = sjson.Set(out, "usage.completion_tokens", outputTokens)
	out, _ = sjson.Set(out, "usage.total_tokens", totalTokens)

	// Add reasoning tokens to usage details if available
	if reasoningTokens > 0 {
		out, _ = sjson.Set(out, "usage.completion_tokens_details.reasoning_tokens", reasoningTokens)
	}

	return out
}
