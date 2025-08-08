// Package codex provides response translation functionality for converting between
// Codex API response formats and OpenAI-compatible formats. It handles both
// streaming and non-streaming responses, transforming backend client responses
// into OpenAI Server-Sent Events (SSE) format and standard JSON response formats.
// The package supports content translation, function calls, reasoning content,
// usage metadata, and various response attributes while maintaining compatibility
// with OpenAI API specifications.
package openai

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ConvertCliToOpenAIParams struct {
	ResponseID string
	CreatedAt  int64
	Model      string
}

// ConvertCodexResponseToOpenAIChat translates a single chunk of a streaming response from the
// Codex backend client format to the OpenAI Server-Sent Events (SSE) format.
// It returns an empty string if the chunk contains no useful data.
func ConvertCodexResponseToOpenAIChat(rawJSON []byte, params *ConvertCliToOpenAIParams) (*ConvertCliToOpenAIParams, string) {
	// Initialize the OpenAI SSE template.
	template := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	rootResult := gjson.ParseBytes(rawJSON)

	typeResult := rootResult.Get("type")
	dataType := typeResult.String()
	if dataType == "response.created" {
		return &ConvertCliToOpenAIParams{
			ResponseID: rootResult.Get("response.id").String(),
			CreatedAt:  rootResult.Get("response.created_at").Int(),
			Model:      rootResult.Get("response.model").String(),
		}, ""
	}

	if params == nil {
		return params, ""
	}

	// Extract and set the model version.
	if modelResult := gjson.GetBytes(rawJSON, "model"); modelResult.Exists() {
		template, _ = sjson.Set(template, "model", modelResult.String())
	}

	template, _ = sjson.Set(template, "created", params.CreatedAt)

	// Extract and set the response ID.
	template, _ = sjson.Set(template, "id", params.ResponseID)

	// Extract and set usage metadata (token counts).
	if usageResult := gjson.GetBytes(rawJSON, "response.usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.prompt_tokens", inputTokensResult.Int())
		}
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	if dataType == "response.reasoning_summary_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.Set(template, "choices.0.delta.reasoning_content", deltaResult.String())
		}
	} else if dataType == "response.reasoning_summary_text.done" {
		template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.Set(template, "choices.0.delta.reasoning_content", "\n\n")
	} else if dataType == "response.output_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.Set(template, "choices.0.delta.content", deltaResult.String())
		}
	} else if dataType == "response.completed" {
		template, _ = sjson.Set(template, "choices.0.finish_reason", "stop")
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", "stop")
	} else if dataType == "response.output_item.done" {
		functionCallItemTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`
		itemResult := rootResult.Get("item")
		if itemResult.Exists() {
			if itemResult.Get("type").String() != "function_call" {
				return params, ""
			}
			template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls", `[]`)
			functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "id", itemResult.Get("call_id").String())
			functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.name", itemResult.Get("name").String())
			functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.arguments", itemResult.Get("arguments").String())
			template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)
		}

	} else {
		return params, ""
	}

	return params, template
}

// ConvertCodexResponseToOpenAIChatNonStream aggregates response from the Codex backend client
// convert a single, non-streaming OpenAI-compatible JSON response.
func ConvertCodexResponseToOpenAIChatNonStream(rawJSON string, unixTimestamp int64) string {
	template := `{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	// Extract and set the model version.
	if modelResult := gjson.Get(rawJSON, "model"); modelResult.Exists() {
		template, _ = sjson.Set(template, "model", modelResult.String())
	}

	// Extract and set the creation timestamp.
	if createdAtResult := gjson.Get(rawJSON, "created_at"); createdAtResult.Exists() {
		template, _ = sjson.Set(template, "created", createdAtResult.Int())
	} else {
		template, _ = sjson.Set(template, "created", unixTimestamp)
	}

	// Extract and set the response ID.
	if idResult := gjson.Get(rawJSON, "id"); idResult.Exists() {
		template, _ = sjson.Set(template, "id", idResult.String())
	}

	// Extract and set usage metadata (token counts).
	if usageResult := gjson.Get(rawJSON, "usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.prompt_tokens", inputTokensResult.Int())
		}
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	// Process the output array for content and function calls
	outputResult := gjson.Get(rawJSON, "output")
	if outputResult.IsArray() {
		outputArray := outputResult.Array()
		var contentText string
		var reasoningText string
		var toolCalls []string

		for _, outputItem := range outputArray {
			outputType := outputItem.Get("type").String()

			switch outputType {
			case "reasoning":
				// Extract reasoning content from summary
				if summaryResult := outputItem.Get("summary"); summaryResult.IsArray() {
					summaryArray := summaryResult.Array()
					for _, summaryItem := range summaryArray {
						if summaryItem.Get("type").String() == "summary_text" {
							reasoningText = summaryItem.Get("text").String()
							break
						}
					}
				}
			case "message":
				// Extract message content
				if contentResult := outputItem.Get("content"); contentResult.IsArray() {
					contentArray := contentResult.Array()
					for _, contentItem := range contentArray {
						if contentItem.Get("type").String() == "output_text" {
							contentText = contentItem.Get("text").String()
							break
						}
					}
				}
			case "function_call":
				// Handle function call content
				functionCallTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`

				if callIdResult := outputItem.Get("call_id"); callIdResult.Exists() {
					functionCallTemplate, _ = sjson.Set(functionCallTemplate, "id", callIdResult.String())
				}

				if nameResult := outputItem.Get("name"); nameResult.Exists() {
					functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.name", nameResult.String())
				}

				if argsResult := outputItem.Get("arguments"); argsResult.Exists() {
					functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.arguments", argsResult.String())
				}

				toolCalls = append(toolCalls, functionCallTemplate)
			}
		}

		// Set content and reasoning content if found
		if contentText != "" {
			template, _ = sjson.Set(template, "choices.0.message.content", contentText)
			template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
		}

		if reasoningText != "" {
			template, _ = sjson.Set(template, "choices.0.message.reasoning_content", reasoningText)
			template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
		}

		// Add tool calls if any
		if len(toolCalls) > 0 {
			template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls", `[]`)
			for _, toolCall := range toolCalls {
				template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls.-1", toolCall)
			}
			template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
		}
	}

	// Extract and set the finish reason based on status
	if statusResult := gjson.Get(rawJSON, "status"); statusResult.Exists() {
		status := statusResult.String()
		if status == "completed" {
			template, _ = sjson.Set(template, "choices.0.finish_reason", "stop")
			template, _ = sjson.Set(template, "choices.0.native_finish_reason", "stop")
		}
	}

	return template
}
