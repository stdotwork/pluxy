package openaichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestAssistantAnnotationsStrippedByDefault(t *testing.T) {
	in := `{"model":"mistral-large-latest","messages":[
		{"role":"user","content":"Hi"},
		{"role":"assistant","content":"Tokyo info","name":"helper","annotations":[
			{"type":"url_citation","url_citation":{"url":"https://example.com/tokyo","title":"Tokyo","start_index":0,"end_index":5}}
		]},
		{"role":"user","content":"Call tool"},
		{"role":"assistant","annotations":[],"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"}
	]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"annotations"`) {
		t.Fatalf("default EncodeRequest leaked annotations: %s", body)
	}
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 5 {
		t.Fatalf("got %d messages, want 5: %s", len(out.Messages), body)
	}
	if out.Messages[1]["content"] != "Tokyo info" || out.Messages[1]["name"] != "helper" {
		t.Errorf("messages[1] lost sibling fields: %v", out.Messages[1])
	}
	if _, ok := out.Messages[3]["tool_calls"]; !ok {
		t.Errorf("messages[3] lost tool_calls: %v", out.Messages[3])
	}
	wantPaths := map[string]bool{
		"messages[1].annotations": false,
		"messages[3].annotations": false,
	}
	for _, l := range losses {
		if l.Kind == llm.LossDropped {
			t.Errorf("unexpected dropped loss %+v; stripping annotations must be approximated", l)
		}
		if l.Kind == llm.LossApproximated {
			if _, ok := wantPaths[l.Path]; ok {
				wantPaths[l.Path] = true
			}
		}
	}
	for p, seen := range wantPaths {
		if !seen {
			t.Errorf("missing approximated loss for %s; got losses %+v", p, losses)
		}
	}
}

func TestAssistantAnnotationsKeptWhenAllowed(t *testing.T) {
	in := `{"model":"openrouter/auto","messages":[
		{"role":"user","content":"Summarize this PDF"},
		{"role":"assistant","content":"Summary","annotations":[
			{"type":"file","file":{"hash":"abc123","name":"doc.pdf"}}
		]},
		{"role":"user","content":"More details"}
	]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequestWith(req, EncodeOptions{AssistantAnnotations: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("expected no losses when AssistantAnnotations=true, got %+v", losses)
	}
	if !strings.Contains(string(body), `"annotations"`) || !strings.Contains(string(body), `"hash":"abc123"`) {
		t.Fatalf("expected annotations to be preserved: %s", body)
	}

	// Requests without annotations stay byte-identical regardless of AssistantAnnotations.
	plainIn := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello","name":"bot"}]}`
	plainReq, err := ParseRequest([]byte(plainIn))
	if err != nil {
		t.Fatal(err)
	}
	bOff, lOff, err := EncodeRequestWith(plainReq, EncodeOptions{AssistantAnnotations: false})
	if err != nil {
		t.Fatal(err)
	}
	bOn, lOn, err := EncodeRequestWith(plainReq, EncodeOptions{AssistantAnnotations: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(lOff) != 0 || len(lOn) != 0 {
		t.Errorf("unexpected losses on plain request: off=%+v on=%+v", lOff, lOn)
	}
	if !bytes.Equal(bOff, bOn) {
		t.Errorf("plain request differs between AssistantAnnotations false and true:\n off: %s\n  on: %s", bOff, bOn)
	}
}
