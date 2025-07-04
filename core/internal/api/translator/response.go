package translator

import (
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCliToOpenAI translates a single chunk of a streaming response from the
// backend client format to the OpenAI Server-Sent Events (SSE) format.
// It returns an empty string if the chunk contains no useful data.
func ConvertCliToOpenAI(rawJson []byte) string {
	// Initialize the OpenAI SSE template.
	template := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	// Extract and set the model version.
	if modelVersionResult := gjson.GetBytes(rawJson, "response.modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	// Extract and set the creation timestamp.
	if createTimeResult := gjson.GetBytes(rawJson, "response.createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		unixTimestamp := time.Now().Unix()
		if err == nil {
			unixTimestamp = t.Unix()
		}
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
	partResult := gjson.GetBytes(rawJson, "response.candidates.0.content.parts.0")
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
		functionCallTemplate := `[{"id": "","type": "function","function": {"name": "","arguments": ""}}]`
		fcName := functionCallResult.Get("name").String()
		functionCallTemplate, _ = sjson.Set(functionCallTemplate, "0.id", fcName)
		functionCallTemplate, _ = sjson.Set(functionCallTemplate, "0.function.name", fcName)
		if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
			functionCallTemplate, _ = sjson.Set(functionCallTemplate, "0.function.arguments", fcArgsResult.Raw)
		}
		template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls", functionCallTemplate)
	} else {
		// If no usable content is found, return an empty string.
		return ""
	}

	return template
}

// ConvertCliToOpenAINonStream aggregates response chunks from the backend client
// into a single, non-streaming OpenAI-compatible JSON response.
func ConvertCliToOpenAINonStream(template string, rawJson []byte) string {
	// Extract and set metadata fields that are typically set once per response.
	if gjson.Get(template, "id").String() == "" {
		if modelVersionResult := gjson.GetBytes(rawJson, "response.modelVersion"); modelVersionResult.Exists() {
			template, _ = sjson.Set(template, "model", modelVersionResult.String())
		}
		if createTimeResult := gjson.GetBytes(rawJson, "response.createTime"); createTimeResult.Exists() {
			t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
			unixTimestamp := time.Now().Unix()
			if err == nil {
				unixTimestamp = t.Unix()
			}
			template, _ = sjson.Set(template, "created", unixTimestamp)
		}
		if responseIdResult := gjson.GetBytes(rawJson, "response.responseId"); responseIdResult.Exists() {
			template, _ = sjson.Set(template, "id", responseIdResult.String())
		}
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
	partResult := gjson.GetBytes(rawJson, "response.candidates.0.content.parts.0")
	partTextResult := partResult.Get("text")
	functionCallResult := partResult.Get("functionCall")

	if partTextResult.Exists() {
		// Append text content, distinguishing between regular content and reasoning.
		if partResult.Get("thought").Bool() {
			currentContent := gjson.Get(template, "choices.0.message.reasoning_content").String()
			template, _ = sjson.Set(template, "choices.0.message.reasoning_content", currentContent+partTextResult.String())
		} else {
			currentContent := gjson.Get(template, "choices.0.message.content").String()
			template, _ = sjson.Set(template, "choices.0.message.content", currentContent+partTextResult.String())
		}
		template, _ = sjson.Set(template, "choices.0.message.role", "assistant")
	} else if functionCallResult.Exists() {
		// Append function call content to the tool_calls array.
		if !gjson.Get(template, "choices.0.message.tool_calls").Exists() {
			template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls", `[]`)
		}
		functionCallItemTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`
		fcName := functionCallResult.Get("name").String()
		functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "id", fcName)
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

	return template
}
