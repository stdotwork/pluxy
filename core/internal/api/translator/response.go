package translator

import (
	"bytes"
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

// ConvertCliToClaude performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates backend client responses
// into Claude-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
func ConvertCliToClaude(rawJson []byte, isGlAPIKey, hasFirstResponse bool, responseType, responseIndex *int) string {
	// Normalize the response format for different API key types
	// Generative Language API keys have a different response structure
	if isGlAPIKey {
		rawJson, _ = sjson.SetRawBytes(rawJson, "response", rawJson)
	}

	// Track whether tools are being used in this response chunk
	usedTool := false
	output := ""

	// Initialize the streaming session with a message_start event
	// This is only sent for the very first response chunk
	if !hasFirstResponse {
		output = "event: message_start\n"

		// Create the initial message structure with default values
		// This follows the Claude API specification for streaming message initialization
		messageStartTemplate := `{"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-3-5-sonnet-20241022", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 0, "output_tokens": 0}}}`

		// Override default values with actual response metadata if available
		if modelVersionResult := gjson.GetBytes(rawJson, "response.modelVersion"); modelVersionResult.Exists() {
			messageStartTemplate, _ = sjson.Set(messageStartTemplate, "message.model", modelVersionResult.String())
		}
		if responseIdResult := gjson.GetBytes(rawJson, "response.responseId"); responseIdResult.Exists() {
			messageStartTemplate, _ = sjson.Set(messageStartTemplate, "message.id", responseIdResult.String())
		}
		output = output + fmt.Sprintf("data: %s\n\n\n", messageStartTemplate)
	}

	// Process the response parts array from the backend client
	// Each part can contain text content, thinking content, or function calls
	partsResult := gjson.GetBytes(rawJson, "response.candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]

			// Extract the different types of content from each part
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")

			// Handle text content (both regular content and thinking)
			if partTextResult.Exists() {
				// Process thinking content (internal reasoning)
				if partResult.Get("thought").Bool() {
					// Continue existing thinking block
					if *responseType == 2 {
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, *responseIndex), "delta.thinking", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
					} else {
						// Transition from another state to thinking
						// First, close any existing content block
						if *responseType != 0 {
							if *responseType == 2 {
								output = output + "event: content_block_delta\n"
								output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, *responseIndex)
								output = output + "\n\n\n"
							}
							output = output + "event: content_block_stop\n"
							output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, *responseIndex)
							output = output + "\n\n\n"
							*responseIndex++
						}

						// Start a new thinking content block
						output = output + "event: content_block_start\n"
						output = output + fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, *responseIndex)
						output = output + "\n\n\n"
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, *responseIndex), "delta.thinking", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
						*responseType = 2 // Set state to thinking
					}
				} else {
					// Process regular text content (user-visible output)
					// Continue existing text block
					if *responseType == 1 {
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, *responseIndex), "delta.text", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
					} else {
						// Transition from another state to text content
						// First, close any existing content block
						if *responseType != 0 {
							if *responseType == 2 {
								output = output + "event: content_block_delta\n"
								output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, *responseIndex)
								output = output + "\n\n\n"
							}
							output = output + "event: content_block_stop\n"
							output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, *responseIndex)
							output = output + "\n\n\n"
							*responseIndex++
						}

						// Start a new text content block
						output = output + "event: content_block_start\n"
						output = output + fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, *responseIndex)
						output = output + "\n\n\n"
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, *responseIndex), "delta.text", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
						*responseType = 1 // Set state to content
					}
				}
			} else if functionCallResult.Exists() {
				// Handle function/tool calls from the AI model
				// This processes tool usage requests and formats them for Claude API compatibility
				usedTool = true
				fcName := functionCallResult.Get("name").String()

				// Handle state transitions when switching to function calls
				// Close any existing function call block first
				if *responseType == 3 {
					output = output + "event: content_block_stop\n"
					output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, *responseIndex)
					output = output + "\n\n\n"
					*responseIndex++
					*responseType = 0
				}

				// Special handling for thinking state transition
				if *responseType == 2 {
					output = output + "event: content_block_delta\n"
					output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, *responseIndex)
					output = output + "\n\n\n"
				}

				// Close any other existing content block
				if *responseType != 0 {
					output = output + "event: content_block_stop\n"
					output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, *responseIndex)
					output = output + "\n\n\n"
					*responseIndex++
				}

				// Start a new tool use content block
				// This creates the structure for a function call in Claude format
				output = output + "event: content_block_start\n"

				// Create the tool use block with unique ID and function details
				data := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`, *responseIndex)
				data, _ = sjson.Set(data, "content_block.id", fmt.Sprintf("%s-%d", fcName, time.Now().UnixNano()))
				data, _ = sjson.Set(data, "content_block.name", fcName)
				output = output + fmt.Sprintf("data: %s\n\n\n", data)

				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					output = output + "event: content_block_delta\n"
					data, _ = sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, *responseIndex), "delta.partial_json", fcArgsResult.Raw)
					output = output + fmt.Sprintf("data: %s\n\n\n", data)
				}
				*responseType = 3
			}
		}
	}

	usageResult := gjson.GetBytes(rawJson, "response.usageMetadata")
	if usageResult.Exists() && bytes.Contains(rawJson, []byte(`"finishReason"`)) {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			output = output + "event: content_block_stop\n"
			output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, *responseIndex)
			output = output + "\n\n\n"

			output = output + "event: message_delta\n"
			output = output + `data: `

			template := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
			if usedTool {
				template = `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
			}

			thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
			template, _ = sjson.Set(template, "usage.output_tokens", candidatesTokenCountResult.Int()+thoughtsTokenCount)
			template, _ = sjson.Set(template, "usage.input_tokens", usageResult.Get("promptTokenCount").Int())

			output = output + template + "\n\n\n"
		}
	}

	return output
}
