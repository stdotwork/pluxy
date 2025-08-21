// Package claude provides response translation functionality for OpenAI to Anthropic API.
// This package handles the conversion of OpenAI Chat Completions API responses into Anthropic API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Anthropic API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package claude

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/luispater/CLIProxyAPI/internal/util"
	"github.com/tidwall/gjson"
)

// ConvertOpenAIResponseToAnthropicParams holds parameters for response conversion
type ConvertOpenAIResponseToAnthropicParams struct {
	MessageID string
	Model     string
	CreatedAt int64
	// Content accumulator for streaming
	ContentAccumulator strings.Builder
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	// Track if text content block has been started
	TextContentBlockStarted bool
	// Track finish reason for later use
	FinishReason string
	// Track if content blocks have been stopped
	ContentBlocksStopped bool
	// Track if message_delta has been sent
	MessageDeltaSent bool
}

// ToolCallAccumulator holds the state for accumulating tool call data
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// ConvertOpenAIResponseToClaude converts OpenAI streaming response format to Anthropic API format.
// This function processes OpenAI streaming chunks and transforms them into Anthropic-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Anthropic API format.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - []string: A slice of strings, each containing an Anthropic-compatible JSON response.
func ConvertOpenAIResponseToClaude(_ context.Context, _ string, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &ConvertOpenAIResponseToAnthropicParams{
			MessageID:               "",
			Model:                   "",
			CreatedAt:               0,
			ContentAccumulator:      strings.Builder{},
			ToolCallsAccumulator:    nil,
			TextContentBlockStarted: false,
			FinishReason:            "",
			ContentBlocksStopped:    false,
			MessageDeltaSent:        false,
		}
	}

	// Check if this is the [DONE] marker
	rawStr := strings.TrimSpace(string(rawJSON))
	if rawStr == "[DONE]" {
		return convertOpenAIDoneToAnthropic((*param).(*ConvertOpenAIResponseToAnthropicParams))
	}

	root := gjson.ParseBytes(rawJSON)

	// Check if this is a streaming chunk or non-streaming response
	objectType := root.Get("object").String()

	if objectType == "chat.completion.chunk" {
		// Handle streaming response
		return convertOpenAIStreamingChunkToAnthropic(rawJSON, (*param).(*ConvertOpenAIResponseToAnthropicParams))
	} else if objectType == "chat.completion" {
		// Handle non-streaming response
		return convertOpenAINonStreamingToAnthropic(rawJSON)
	}

	return []string{}
}

// convertOpenAIStreamingChunkToAnthropic converts OpenAI streaming chunk to Anthropic streaming events
func convertOpenAIStreamingChunkToAnthropic(rawJSON []byte, param *ConvertOpenAIResponseToAnthropicParams) []string {
	root := gjson.ParseBytes(rawJSON)
	var results []string

	// Initialize parameters if needed
	if param.MessageID == "" {
		param.MessageID = root.Get("id").String()
	}
	if param.Model == "" {
		param.Model = root.Get("model").String()
	}
	if param.CreatedAt == 0 {
		param.CreatedAt = root.Get("created").Int()
	}

	// Check if this is the first chunk (has role)
	if delta := root.Get("choices.0.delta"); delta.Exists() {
		if role := delta.Get("role"); role.Exists() && role.String() == "assistant" {
			// Send message_start event
			messageStart := map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":            param.MessageID,
					"type":          "message",
					"role":          "assistant",
					"model":         param.Model,
					"content":       []interface{}{},
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage": map[string]interface{}{
						"input_tokens":  0,
						"output_tokens": 0,
					},
				},
			}
			messageStartJSON, _ := json.Marshal(messageStart)
			results = append(results, "event: message_start\ndata: "+string(messageStartJSON)+"\n\n")

			// Don't send content_block_start for text here - wait for actual content
		}

		// Handle content delta
		if content := delta.Get("content"); content.Exists() && content.String() != "" {
			// Send content_block_start for text if not already sent
			if !param.TextContentBlockStarted {
				contentBlockStart := map[string]interface{}{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				}
				contentBlockStartJSON, _ := json.Marshal(contentBlockStart)
				results = append(results, "event: content_block_start\ndata: "+string(contentBlockStartJSON)+"\n\n")
				param.TextContentBlockStarted = true
			}

			contentDelta := map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": content.String(),
				},
			}
			contentDeltaJSON, _ := json.Marshal(contentDelta)
			results = append(results, "event: content_block_delta\ndata: "+string(contentDeltaJSON)+"\n\n")

			// Accumulate content
			param.ContentAccumulator.WriteString(content.String())
		}

		// Handle tool calls
		if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			if param.ToolCallsAccumulator == nil {
				param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}

			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				index := int(toolCall.Get("index").Int())

				// Initialize accumulator if needed
				if _, exists := param.ToolCallsAccumulator[index]; !exists {
					param.ToolCallsAccumulator[index] = &ToolCallAccumulator{}
				}

				accumulator := param.ToolCallsAccumulator[index]

				// Handle tool call ID
				if id := toolCall.Get("id"); id.Exists() {
					accumulator.ID = id.String()
				}

				// Handle function name
				if function := toolCall.Get("function"); function.Exists() {
					if name := function.Get("name"); name.Exists() {
						accumulator.Name = name.String()

						if param.TextContentBlockStarted {
							param.TextContentBlockStarted = false
							contentBlockStop := map[string]interface{}{
								"type":  "content_block_stop",
								"index": index,
							}
							contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
							results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
						}

						// Send content_block_start for tool_use
						contentBlockStart := map[string]interface{}{
							"type":  "content_block_start",
							"index": index + 1, // Offset by 1 since text is at index 0
							"content_block": map[string]interface{}{
								"type":  "tool_use",
								"id":    accumulator.ID,
								"name":  accumulator.Name,
								"input": map[string]interface{}{},
							},
						}
						contentBlockStartJSON, _ := json.Marshal(contentBlockStart)
						results = append(results, "event: content_block_start\ndata: "+string(contentBlockStartJSON)+"\n\n")
					}

					// Handle function arguments
					if args := function.Get("arguments"); args.Exists() {
						argsText := args.String()
						if argsText != "" {
							accumulator.Arguments.WriteString(argsText)
						}
					}
				}

				return true
			})
		}
	}

	// Handle finish_reason (but don't send message_delta/message_stop yet)
	if finishReason := root.Get("choices.0.finish_reason"); finishReason.Exists() && finishReason.String() != "" {
		reason := finishReason.String()
		param.FinishReason = reason

		// Send content_block_stop for text if text content block was started
		if param.TextContentBlockStarted && !param.ContentBlocksStopped {
			contentBlockStop := map[string]interface{}{
				"type":  "content_block_stop",
				"index": 0,
			}
			contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
			results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
		}

		// Send content_block_stop for any tool calls
		if !param.ContentBlocksStopped {
			for index := range param.ToolCallsAccumulator {
				accumulator := param.ToolCallsAccumulator[index]

				// Send complete input_json_delta with all accumulated arguments
				if accumulator.Arguments.Len() > 0 {
					inputDelta := map[string]interface{}{
						"type":  "content_block_delta",
						"index": index + 1,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": util.FixJSON(accumulator.Arguments.String()),
						},
					}
					inputDeltaJSON, _ := json.Marshal(inputDelta)
					results = append(results, "event: content_block_delta\ndata: "+string(inputDeltaJSON)+"\n\n")
				}

				contentBlockStop := map[string]interface{}{
					"type":  "content_block_stop",
					"index": index + 1,
				}
				contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
				results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
			}
			param.ContentBlocksStopped = true
		}

		// Don't send message_delta here - wait for usage info or [DONE]
	}

	// Handle usage information separately (this comes in a later chunk)
	// Only process if usage has actual values (not null)
	if usage := root.Get("usage"); usage.Exists() && usage.Type != gjson.Null && param.FinishReason != "" {
		// Check if usage has actual token counts
		promptTokens := usage.Get("prompt_tokens")
		completionTokens := usage.Get("completion_tokens")

		if promptTokens.Exists() && completionTokens.Exists() {
			// Send message_delta with usage
			messageDelta := map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   mapOpenAIFinishReasonToAnthropic(param.FinishReason),
					"stop_sequence": nil,
				},
				"usage": map[string]interface{}{
					"input_tokens":  promptTokens.Int(),
					"output_tokens": completionTokens.Int(),
				},
			}

			messageDeltaJSON, _ := json.Marshal(messageDelta)
			results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
			param.MessageDeltaSent = true
		}
	}

	return results
}

// convertOpenAIDoneToAnthropic handles the [DONE] marker and sends final events
func convertOpenAIDoneToAnthropic(param *ConvertOpenAIResponseToAnthropicParams) []string {
	var results []string

	// If we haven't sent message_delta yet (no usage info was received), send it now
	if param.FinishReason != "" && !param.MessageDeltaSent {
		messageDelta := map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason":   mapOpenAIFinishReasonToAnthropic(param.FinishReason),
				"stop_sequence": nil,
			},
		}

		messageDeltaJSON, _ := json.Marshal(messageDelta)
		results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
		param.MessageDeltaSent = true
	}

	// Send message_stop
	results = append(results, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	return results
}

// convertOpenAINonStreamingToAnthropic converts OpenAI non-streaming response to Anthropic format
func convertOpenAINonStreamingToAnthropic(rawJSON []byte) []string {
	root := gjson.ParseBytes(rawJSON)

	// Build Anthropic response
	response := map[string]interface{}{
		"id":            root.Get("id").String(),
		"type":          "message",
		"role":          "assistant",
		"model":         root.Get("model").String(),
		"content":       []interface{}{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}

	// Process message content and tool calls
	var contentBlocks []interface{}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choice := choices.Array()[0] // Take first choice

		// Handle text content
		if content := choice.Get("message.content"); content.Exists() && content.String() != "" {
			textBlock := map[string]interface{}{
				"type": "text",
				"text": content.String(),
			}
			contentBlocks = append(contentBlocks, textBlock)
		}

		// Handle tool calls
		if toolCalls := choice.Get("message.tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				toolUseBlock := map[string]interface{}{
					"type": "tool_use",
					"id":   toolCall.Get("id").String(),
					"name": toolCall.Get("function.name").String(),
				}

				// Parse arguments
				argsStr := toolCall.Get("function.arguments").String()
				argsStr = util.FixJSON(argsStr)
				if argsStr != "" {
					var args interface{}
					if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
						toolUseBlock["input"] = args
					} else {
						toolUseBlock["input"] = map[string]interface{}{}
					}
				} else {
					toolUseBlock["input"] = map[string]interface{}{}
				}

				contentBlocks = append(contentBlocks, toolUseBlock)
				return true
			})
		}

		// Set stop reason
		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			response["stop_reason"] = mapOpenAIFinishReasonToAnthropic(finishReason.String())
		}
	}

	response["content"] = contentBlocks

	// Set usage information
	if usage := root.Get("usage"); usage.Exists() {
		response["usage"] = map[string]interface{}{
			"input_tokens":  usage.Get("prompt_tokens").Int(),
			"output_tokens": usage.Get("completion_tokens").Int(),
		}
	}

	responseJSON, _ := json.Marshal(response)
	return []string{string(responseJSON)}
}

// mapOpenAIFinishReasonToAnthropic maps OpenAI finish reasons to Anthropic equivalents
func mapOpenAIFinishReasonToAnthropic(openAIReason string) string {
	switch openAIReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn" // Anthropic doesn't have direct equivalent
	case "function_call": // Legacy OpenAI
		return "tool_use"
	default:
		return "end_turn"
	}
}

// ConvertOpenAIResponseToClaudeNonStream converts a non-streaming OpenAI response to a non-streaming Anthropic response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - string: An Anthropic-compatible JSON response.
func ConvertOpenAIResponseToClaudeNonStream(_ context.Context, _ string, _ []byte, _ *any) string {
	return ""
}
