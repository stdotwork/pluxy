package geminigenerate

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A functionResponse's parts are read into the result's parts after its response text.
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
