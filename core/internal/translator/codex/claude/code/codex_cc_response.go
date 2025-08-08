// Package code provides response translation functionality for Claude API.
// This package handles the conversion of backend client responses into Claude-compatible
// Server-Sent Events (SSE) format, implementing a sophisticated state machine that manages
// different response types including text content, thinking processes, and function calls.
// The translation ensures proper sequencing of SSE events and maintains state across
// multiple response chunks to provide a seamless streaming experience.
package code

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCliToClaude performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates backend client responses
// into Claude-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
func ConvertCodexResponseToClaude(rawJSON []byte, hasToolCall bool) (string, bool) {
	// log.Debugf("rawJSON: %s", string(rawJSON))
	output := ""
	rootResult := gjson.ParseBytes(rawJSON)
	typeResult := rootResult.Get("type")
	typeStr := typeResult.String()
	template := ""
	if typeStr == "response.created" {
		template = `{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"claude-opus-4-1-20250805","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0},"content":[],"stop_reason":null}}`
		template, _ = sjson.Set(template, "message.model", rootResult.Get("response.model").String())
		template, _ = sjson.Set(template, "message.id", rootResult.Get("response.id").String())

		output = "event: message_start\n"
		output += fmt.Sprintf("data: %s\n", template)
	} else if typeStr == "response.reasoning_summary_part.added" {
		template = `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`
		template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())

		output = "event: content_block_start\n"
		output += fmt.Sprintf("data: %s\n", template)
	} else if typeStr == "response.reasoning_summary_text.delta" {
		template = `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`
		template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())
		template, _ = sjson.Set(template, "delta.thinking", rootResult.Get("delta").String())

		output = "event: content_block_delta\n"
		output += fmt.Sprintf("data: %s\n", template)
	} else if typeStr == "response.reasoning_summary_part.done" {
		template = `{"type":"content_block_stop","index":0}`
		template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())

		output = "event: content_block_stop\n"
		output += fmt.Sprintf("data: %s\n", template)
	} else if typeStr == "response.content_part.added" {
		template = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
		template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())

		output = "event: content_block_start\n"
		output += fmt.Sprintf("data: %s\n", template)
	} else if typeStr == "response.output_text.delta" {
		template = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`
		template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())
		template, _ = sjson.Set(template, "delta.text", rootResult.Get("delta").String())

		output = "event: content_block_delta\n"
		output += fmt.Sprintf("data: %s\n", template)
	} else if typeStr == "response.content_part.done" {
		template = `{"type":"content_block_stop","index":0}`
		template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())

		output = "event: content_block_stop\n"
		output += fmt.Sprintf("data: %s\n", template)
	} else if typeStr == "response.completed" {
		template = `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
		if hasToolCall {
			template, _ = sjson.Set(template, "delta.stop_reason", "tool_use")
		} else {
			template, _ = sjson.Set(template, "delta.stop_reason", "end_turn")
		}
		template, _ = sjson.Set(template, "usage.input_tokens", rootResult.Get("response.usage.input_tokens").Int())
		template, _ = sjson.Set(template, "usage.output_tokens", rootResult.Get("response.usage.output_tokens").Int())

		output = "event: message_delta\n"
		output += fmt.Sprintf("data: %s\n\n", template)
		output += "event: message_stop\n"
		output += `data: {"type":"message_stop"}`
		output += "\n\n"
	} else if typeStr == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "function_call" {
			hasToolCall = true
			template = `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`
			template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())
			template, _ = sjson.Set(template, "content_block.id", itemResult.Get("call_id").String())
			template, _ = sjson.Set(template, "content_block.name", itemResult.Get("name").String())

			output = "event: content_block_start\n"
			output += fmt.Sprintf("data: %s\n\n", template)

			template = `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
			template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())

			output += "event: content_block_delta\n"
			output += fmt.Sprintf("data: %s\n", template)
		}
	} else if typeStr == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "function_call" {
			template = `{"type":"content_block_stop","index":0}`
			template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())

			output = "event: content_block_stop\n"
			output += fmt.Sprintf("data: %s\n", template)
		}
	} else if typeStr == "response.function_call_arguments.delta" {
		template = `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
		template, _ = sjson.Set(template, "index", rootResult.Get("output_index").Int())
		template, _ = sjson.Set(template, "delta.partial_json", rootResult.Get("delta").String())

		output += "event: content_block_delta\n"
		output += fmt.Sprintf("data: %s\n", template)
	}

	return output, hasToolCall
}
