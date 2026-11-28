package geminigenerate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestParseFunctionResponseParts(t *testing.T) {
	req, err := ParseRequest("m", false, []byte(`{"contents":[
	 {"role":"model","parts":[{"functionCall":{"id":"c1","name":"shot","args":{}}}]},
	 {"role":"user","parts":[{"functionResponse":{"id":"c1","name":"shot","response":{"result":"ok"},
	  "parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	it := req.Items[len(req.Items)-1]
	if it.Type != llm.ItemTypeToolResult || it.Output != `{"result":"ok"}` || len(it.Content) != 2 {
		t.Fatalf("item = %+v, want the response as output and 2 parts", it)
	}
	if p := it.Content[0]; p.Type != llm.PartText || p.Text != it.Output {
		t.Errorf("part 0 = %+v, want the response text", p)
	}
	if p := it.Content[1]; p.Type != llm.PartImage || p.URL != "data:image/png;base64,AAAA" {
		t.Errorf("part 1 = %+v, want the image", p)
	}
	if it.Origin != nil && len(it.Origin.Raw) > 0 {
		t.Errorf("raw = %s, want nothing unmodelled", it.Origin.Raw)
	}
}

func TestFunctionResponseTextPartOutsideOutput(t *testing.T) {
	req := llm.Request{Model: "m", Items: []llm.Item{
		{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "save", Arguments: "{}",
			Origin: &llm.Origin{Format: format, Path: "contents[0].parts[0]"}},
		{Type: llm.ItemTypeToolResult, CallID: "c1",
			Content: []llm.Part{{Type: llm.PartText, Text: "orphan text"}},
			Origin:  &llm.Origin{Format: format, Path: "contents[1].parts[0]"}},
	}}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "orphan text") {
		t.Errorf("body %s carries text functionResponse.parts cannot hold", body)
	}
	want := llm.Loss{Kind: llm.LossDropped, Path: "contents[1].parts[0].content[0]"}
	if len(losses) != 1 || losses[0].Kind != want.Kind || losses[0].Path != want.Path {
		t.Errorf("losses = %+v, want the text reported dropped at %s", losses, want.Path)
	}
}

func TestFunctionResponseTextPartInOutput(t *testing.T) {
	req, err := anthropicmessages.ParseRequest([]byte(`{"model":"m","max_tokens":5,"messages":[
	 {"role":"user","content":"hi"},
	 {"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"shot","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","is_error":true,"content":[
	  {"type":"text","text":"Saved."},
	  {"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Contents []struct {
			Parts []struct {
				FunctionResponse struct {
					Response json.RawMessage   `json:"response"`
					Parts    []json.RawMessage `json:"parts"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("contents: %v: %s", err, body)
	}
	if len(got.Contents) != 3 || len(got.Contents[2].Parts) != 1 {
		t.Fatalf("contents = %s, want the functionResponse", body)
	}
	fr := got.Contents[2].Parts[0].FunctionResponse
	sameJSON(t, "functionResponse.response", fr.Response, `{"error":"Saved."}`)
	if len(fr.Parts) != 1 {
		t.Fatalf("functionResponse.parts = %s, want the image", fr.Parts)
	}
	for _, l := range losses {
		if strings.Contains(l.Path, ".content[0]") {
			t.Errorf("loss %+v: the text is in the response, so nothing was lost", l)
		}
	}
}
