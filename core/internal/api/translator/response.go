package translator

import (
	"fmt"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCliToOpenAI translates a single chunk of a streaming response from the
// backend client format to the OpenAI Server-Sent Events (SSE) format.
// It returns an empty string if the chunk contains no useful data.
func ConvertCliToOpenAI(rawJson []byte, unixTimestamp int64, isGlAPIKey bool) string {
	if isGlAPIKey {
		rawJson, _ = sjson.SetRawBytes(rawJson, "response", rawJson)
	}

	// Initialize the OpenAI SSE template.
	template := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	// Extract and set the model version.
	if modelVersionResult := gjson.GetBytes(rawJson, "response.modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	// Extract and set the creation timestamp.
	if createTimeResult := gjson.GetBytes(rawJson, "response.createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			unixTimestamp = t.Unix()
		}
		template, _ = sjson.Set(template, "created", unixTimestamp)
	} else {
		template, _ = sjson.Set(template, "created", unixTimestamp)
	}

	// Extract and set the response ID.
	if responseIdResult := gjson.GetBytes(rawJson, "response.responseId"); responseIdResult.Exists() {
		template, _ = sjson.Set(template, "id", responseIdResult.String())
	}

	// Extract and set the finish reason.
	if finishReasonResult := gjson.GetBytes(rawJson, "response.candidates.0.finishReason"); finishReasonResult.Exists() {
		template, _ = sjson.Set(template, "choices.0.finish_reason", finishReasonResult.String())
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", finishReasonResult.String())
	}

	// Extract and set usage metadata (token counts).
	if usageResult := gjson.GetBytes(rawJson, "response.usageMetadata"); usageResult.Exists() {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCount+thoughtsTokenCount)
		if thoughtsTokenCount > 0 {
			template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
	}

	// Process the main content part of the response.
	partsResult := gjson.GetBytes(rawJson, "response.candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")

			if partTextResult.Exists() {
				// Handle text content, distinguishing between regular content and reasoning/thoughts.
				if partResult.Get("thought").Bool() {
					template, _ = sjson.Set(template, "choices.0.delta.reasoning_content", partTextResult.String())
				} else {
					template, _ = sjson.Set(template, "choices.0.delta.content", partTextResult.String())
				}
				template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
			} else if functionCallResult.Exists() {
				// Handle function call content.
				toolCallsResult := gjson.Get(template, "choices.0.delta.tool_calls")
				if !toolCallsResult.Exists() || !toolCallsResult.IsArray() {
					template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls", `[]`)
				}

				functionCallTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`
				fcName := functionCallResult.Get("name").String()
				functionCallTemplate, _ = sjson.Set(functionCallTemplate, "id", fmt.Sprintf("%s-%d", fcName, time.Now().UnixNano()))
				functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.name", fcName)
				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.arguments", fcArgsResult.Raw)
				}
				template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
				template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls.-1", functionCallTemplate)
			}
		}
	}

	return template
}

// ConvertCliToOpenAINonStream aggregates response from the backend client
// convert a single, non-streaming OpenAI-compatible JSON response.
func ConvertCliToOpenAINonStream(rawJson []byte, unixTimestamp int64, isGlAPIKey bool) string {
	if isGlAPIKey {
		rawJson, _ = sjson.SetRawBytes(rawJson, "response", rawJson)
	}
	template := `{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`
	if modelVersionResult := gjson.GetBytes(rawJson, "response.modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	if createTimeResult := gjson.GetBytes(rawJson, "response.createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			unixTimestamp = t.Unix()
		}
		template, _ = sjson.Set(template, "created", unixTimestamp)
	} else {
		template, _ = sjson.Set(template, "created", unixTimestamp)
	}

	if responseIdResult := gjson.GetBytes(rawJson, "response.responseId"); responseIdResult.Exists() {
		template, _ = sjson.Set(template, "id", responseIdResult.String())
	}

	if finishReasonResult := gjson.GetBytes(rawJson, "response.candidates.0.finishReason"); finishReasonResult.Exists() {
		template, _ = sjson.Set(template, "choices.0.finish_reason", finishReasonResult.String())
		template, _ = sjson.Set(template, "choices.0.native_finish_reason", finishReasonResult.String())
	}

	if usageResult := gjson.GetBytes(rawJson, "response.usageMetadata"); usageResult.Exists() {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCount+thoughtsTokenCount)
		if thoughtsTokenCount > 0 {
			template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
	}

	// Process the main content part of the response.
	partsResult := gjson.GetBytes(rawJson, "response.candidates.0.content.parts")
	if partsResult.IsArray() {
		partsResults := partsResult.Array()
		for i := 0; i < len(partsResults); i++ {
			partResult := partsResults[i]
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")

			if partTextResult.Exists() {
				// Append text content, distinguishing between regular content and reasoning.
				if partResult.Get("thought").Bool() {
					template, _ = sjson.Set(template, "choices.0.message.reasoning_content", partTextResult.String())
				} else {
					template, _ = sjson.Set(template, "choices.0.message.content", partTextResult.String())
				}
				template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
			} else if functionCallResult.Exists() {
				// Append function call content to the tool_calls array.
				toolCallsResult := gjson.Get(template, "choices.0.message.tool_calls")
				if !toolCallsResult.Exists() || !toolCallsResult.IsArray() {
					template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls", `[]`)
				}
				functionCallItemTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`
				fcName := functionCallResult.Get("name").String()
				functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "id", fmt.Sprintf("%s-%d", fcName, time.Now().UnixNano()))
				functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.name", fcName)
				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.arguments", fcArgsResult.Raw)
				}
				template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
				template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls.-1", functionCallItemTemplate)
			} else {
				// If no usable content is found, return an empty string.
				return ""
			}
		}
	}

	return template
}
