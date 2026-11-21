package geminigenerate

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/llm"
)

// requestWithErrorResponse holds a Gemini tool call answered by a functionResponse with an error
// key, as Google documents a failed call.
const requestWithErrorResponse = `{"contents":[
 {"role":"model","parts":[{"functionCall":{"id":"c1","name":"save","args":{}}}]},
 {"role":"user","parts":[{"functionResponse":{"id":"c1","name":"save","response":{"error":"disk full"}}}]}]}`

// A functionResponse with an error key is a failed call: the error's value is the result's output,
// as the encoder writes it, and GenerateContent gets the response back unchanged.
func TestFunctionResponseError(t *testing.T) {
	req := mustParse(t, requestWithErrorResponse)
	it := req.Items[len(req.Items)-1]
	if it.Type != llm.ItemTypeToolResult || !it.IsError || it.Output != "disk full" {
		t.Fatalf("tool result = %+v, want the error value as output and is_error", it)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
	var got struct {
		Contents []struct {
			Parts []struct {
				FunctionResponse struct {
					Response json.RawMessage `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("contents: %v: %s", err, body)
	}
	if len(got.Contents) != 2 || len(got.Contents[1].Parts) != 1 {
		t.Fatalf("contents = %s, want the functionResponse", body)
	}
	sameJSON(t, "functionResponse.response", got.Contents[1].Parts[0].FunctionResponse.Response,
		`{"error":"disk full"}`)
}

// Rebuilt as Messages, the same failed call is is_error with the error value as its text.
func TestFunctionResponseErrorToMessages(t *testing.T) {
	req := mustParse(t, requestWithErrorResponse)
	body, _, err := anthropicmessages.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("messages: %v: %s", err, body)
	}
	if len(got.Messages) != 2 || len(got.Messages[1].Content) != 1 {
		t.Fatalf("messages = %s, want the call then its result", body)
	}
	sameJSON(t, "tool_result", got.Messages[1].Content[0],
		`{"type":"tool_result","tool_use_id":"c1","content":"disk full","is_error":true}`)
}
