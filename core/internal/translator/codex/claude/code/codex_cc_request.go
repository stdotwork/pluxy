// Package code provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package code

import (
	"fmt"

	"github.com/luispater/CLIProxyAPI/internal/misc"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PrepareClaudeRequest parses and transforms a Claude API request into internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
func ConvertClaudeCodeRequestToCodex(rawJSON []byte) string {
	template := `{"model":"","instructions":"","input":[]}`

	instructions := misc.CodexInstructions
	template, _ = sjson.SetRaw(template, "instructions", instructions)

	rootResult := gjson.ParseBytes(rawJSON)
	modelResult := rootResult.Get("model")
	template, _ = sjson.Set(template, "model", modelResult.String())

	systemsResult := rootResult.Get("system")
	if systemsResult.IsArray() {
		systemResults := systemsResult.Array()
		message := `{"type":"message","role":"user","content":[]}`
		for i := 0; i < len(systemResults); i++ {
			systemResult := systemResults[i]
			systemTypeResult := systemResult.Get("type")
			if systemTypeResult.String() == "text" {
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.type", i), "input_text")
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.text", i), systemResult.Get("text").String())
			}
		}
		template, _ = sjson.SetRaw(template, "input.-1", message)
	}

	messagesResult := rootResult.Get("messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()

		for i := 0; i < len(messageResults); i++ {
			messageResult := messageResults[i]

			messageContentsResult := messageResult.Get("content")
			if messageContentsResult.IsArray() {
				messageContentResults := messageContentsResult.Array()
				for j := 0; j < len(messageContentResults); j++ {
					messageContentResult := messageContentResults[j]
					messageContentTypeResult := messageContentResult.Get("type")
					if messageContentTypeResult.String() == "text" {
						message := `{"type": "message","role":"","content":[]}`
						messageRole := messageResult.Get("role").String()
						message, _ = sjson.Set(message, "role", messageRole)

						partType := "input_text"
						if messageRole == "assistant" {
							partType = "output_text"
						}

						currentIndex := len(gjson.Get(message, "content").Array())
						message, _ = sjson.Set(message, fmt.Sprintf("content.%d.type", currentIndex), partType)
						message, _ = sjson.Set(message, fmt.Sprintf("content.%d.text", currentIndex), messageContentResult.Get("text").String())
						template, _ = sjson.SetRaw(template, "input.-1", message)
					} else if messageContentTypeResult.String() == "tool_use" {
						functionCallMessage := `{"type":"function_call"}`
						functionCallMessage, _ = sjson.Set(functionCallMessage, "call_id", messageContentResult.Get("id").String())
						functionCallMessage, _ = sjson.Set(functionCallMessage, "name", messageContentResult.Get("name").String())
						functionCallMessage, _ = sjson.Set(functionCallMessage, "arguments", messageContentResult.Get("input").Raw)
						template, _ = sjson.SetRaw(template, "input.-1", functionCallMessage)
					} else if messageContentTypeResult.String() == "tool_result" {
						functionCallOutputMessage := `{"type":"function_call_output"}`
						functionCallOutputMessage, _ = sjson.Set(functionCallOutputMessage, "call_id", messageContentResult.Get("tool_use_id").String())
						functionCallOutputMessage, _ = sjson.Set(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
						template, _ = sjson.SetRaw(template, "input.-1", functionCallOutputMessage)
					}
				}
			}
		}

	}

	toolsResult := rootResult.Get("tools")
	if toolsResult.IsArray() {
		template, _ = sjson.SetRaw(template, "tools", `[]`)
		template, _ = sjson.Set(template, "tool_choice", `auto`)
		toolResults := toolsResult.Array()
		for i := 0; i < len(toolResults); i++ {
			toolResult := toolResults[i]
			tool := toolResult.Raw
			tool, _ = sjson.Set(tool, "type", "function")
			tool, _ = sjson.SetRaw(tool, "parameters", toolResult.Get("input_schema").Raw)
			tool, _ = sjson.Delete(tool, "input_schema")
			tool, _ = sjson.Delete(tool, "parameters.$schema")
			tool, _ = sjson.Set(tool, "strict", false)
			template, _ = sjson.SetRaw(template, "tools.-1", tool)
		}
	}

	template, _ = sjson.Set(template, "parallel_tool_calls", true)
	template, _ = sjson.Set(template, "reasoning.effort", "low")
	template, _ = sjson.Set(template, "reasoning.summary", "auto")
	template, _ = sjson.Set(template, "stream", true)
	template, _ = sjson.Set(template, "store", false)
	template, _ = sjson.Set(template, "include", []string{"reasoning.encrypted_content"})

	return template
}
