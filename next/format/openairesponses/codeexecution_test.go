package openairesponses_test

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
)

// These models' pages list code_interpreter, but their Responses endpoint row says "Not supported".
func TestCodeExecutionNotSentToModelsWithoutResponses(t *testing.T) {
	for _, model := range []string{"o1-mini", "gpt-4o-mini-audio-preview"} {
		req, err := anthropicmessages.ParseRequest([]byte(`{"model":"` + model + `","max_tokens":9,
			"messages":[{"role":"user","content":"compute"}],
			"tools":[{"type":"code_execution_20250825","name":"code_execution"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := openairesponses.EncodeRequestWith(req, openairesponses.EncodeOptions{CodeExecution: true})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != 0 {
			t.Errorf("%s: tools = %s, want none", model, got.Tools)
		}
	}
}
