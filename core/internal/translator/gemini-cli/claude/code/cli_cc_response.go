// Package code provides response translation functionality for Claude API.
// This package handles the conversion of backend client responses into Claude-compatible
// Server-Sent Events (SSE) format, implementing a sophisticated state machine that manages
// different response types including text content, thinking processes, and function calls.
// The translation ensures proper sequencing of SSE events and maintains state across
// multiple response chunks to provide a seamless streaming experience.
package code

import (
	"bytes"
	"fmt"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCliResponseToClaudeCode performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates backend client responses
// into Claude-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
func ConvertCliResponseToClaudeCode(rawJSON []byte, isGlAPIKey, hasFirstResponse bool, responseType, responseIndex *int) string {
	// Normalize the response format for different API key types
	// Generative Language API keys have a different response structure
	if isGlAPIKey {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "response", rawJSON)
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
		if modelVersionResult := gjson.GetBytes(rawJSON, "response.modelVersion"); modelVersionResult.Exists() {
			messageStartTemplate, _ = sjson.Set(messageStartTemplate, "message.model", modelVersionResult.String())
		}
		if responseIDResult := gjson.GetBytes(rawJSON, "response.responseId"); responseIDResult.Exists() {
			messageStartTemplate, _ = sjson.Set(messageStartTemplate, "message.id", responseIDResult.String())
		}
		output = output + fmt.Sprintf("data: %s\n\n\n", messageStartTemplate)
	}

	// Process the response parts array from the backend client
	// Each part can contain text content, thinking content, or function calls
	partsResult := gjson.GetBytes(rawJSON, "response.candidates.0.content.parts")
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

	usageResult := gjson.GetBytes(rawJSON, "response.usageMetadata")
	if usageResult.Exists() && bytes.Contains(rawJSON, []byte(`"finishReason"`)) {
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
