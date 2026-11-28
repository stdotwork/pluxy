package openairesponses

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestParseFunctionCallOutputParts(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","input":[
	 {"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},
	 {"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"Saved."},{"type":"input_image","image_url":"https://x.test/a.png","detail":"high"},{"type":"input_file","file_id":"file_1"}]},
	 {"type":"function_call","call_id":"c2","name":"f","arguments":"{}"},
	 {"type":"function_call_output","call_id":"c2","output":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	rich, plain := req.Items[1], req.Items[3]
	if rich.Output != "Saved." || len(rich.Content) != 3 || rich.Content[1].Type != llm.PartImage || rich.Content[1].Detail != "high" || rich.Content[2].FileID != "file_1" {
		t.Errorf("rich result = %+v, want output Saved. and its 3 parts", rich)
	}
	if rich.Origin != nil && len(rich.Origin.Raw) > 0 {
		t.Errorf("rich result raw = %s, want nothing unmodelled", rich.Origin.Raw)
	}
	if plain.Output != "ab" || len(plain.Content) != 0 {
		t.Errorf("text-only result = %+v, want output ab and no parts", plain)
	}
}
