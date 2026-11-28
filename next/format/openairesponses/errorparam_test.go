package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestErrorEventKeepsParam(t *testing.T) {
	got := errorFields(&llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Message: "m",
		Raw: json.RawMessage(`{"error":{"message":"m","param":"input","code":"c"}}`)})
	if got["param"] != "input" {
		t.Errorf("param = %v, want input", got["param"])
	}
	got = errorFields(&llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Message: "m"})
	if v, ok := got["param"]; !ok || v != nil {
		t.Errorf("param = %v (present %v), want null", v, ok)
	}
}
