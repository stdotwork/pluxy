package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// encodeCallArgs returns the arguments of the single function_call an EncodeRequest built from
// one call writes.
func encodeCallArgs(t *testing.T, it llm.Item) (string, []llm.Loss) {
	t.Helper()
	body, losses, err := EncodeRequest(llm.Request{Model: "m", Items: []llm.Item{it}})
	if err != nil {
		t.Fatalf("%+v: %v", it, err)
	}
	var out struct {
		Input []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if len(out.Input) != 1 || out.Input[0].Type != "function_call" {
		t.Fatalf("body %s has no single function call", body)
	}
	return out.Input[0].Arguments, losses
}

func translatedCall(format, args string) llm.Item {
	return llm.Item{Type: llm.ItemTypeToolCall, CallID: "call_1", Name: "get_weather", Arguments: args,
		Origin: &llm.Origin{Format: format, Path: "items[0]"}}
}

// A call replayed from another format whose arguments are not a JSON object is sent as {}, and
// the loss is reported, as the Messages and Gemini encoders already do.
func TestEncodeRequestRepairsTranslatedToolArguments(t *testing.T) {
	for _, args := range []string{`{"city":`, `[1,2]`, `"Oslo"`, `null`, ``} {
		for _, origin := range []*llm.Origin{{Format: llm.FormatAnthropicMessages, Path: "items[0]"}, nil} {
			it := translatedCall(llm.FormatAnthropicMessages, args)
			it.Origin = origin
			got, losses := encodeCallArgs(t, it)
			if got != "{}" {
				t.Errorf("arguments %q became %q, want {}", args, got)
			}
			wantLoss := args != ""
			if has := len(losses) == 1 && losses[0].Kind == llm.LossApproximated &&
				losses[0].Path == "items[0]" && losses[0].Detail == "tool arguments that are not a JSON object, sent as {}"; has != wantLoss {
				t.Errorf("arguments %q: losses = %+v, want a repair loss: %v", args, losses, wantLoss)
			}
		}
	}
}

// A Responses client's own call keeps its bytes, and a custom tool's input is free-form text.
func TestEncodeRequestKeepsSameFormatAndCustomToolArguments(t *testing.T) {
	for _, it := range []llm.Item{
		translatedCall(llm.FormatOpenAIResponses, `{"city":`),
	} {
		got, losses := encodeCallArgs(t, it)
		if got != `{"city":` {
			t.Errorf("arguments became %q, want the bytes it came with", got)
		}
		if len(losses) != 0 {
			t.Errorf("losses = %+v, want none", losses)
		}
	}

	custom := translatedCall(llm.FormatAnthropicMessages, "*** Begin Patch\n")
	custom.Custom = true
	body, losses, err := EncodeRequest(llm.Request{Model: "m", Items: []llm.Item{custom}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"input":"*** Begin Patch\n"`; !json.Valid(body) || !containsJSON(body, want) {
		t.Errorf("body %s does not hold the custom input %s", body, want)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

// containsJSON reports whether body holds want as text: a JSON body of a custom call holds its
// input string as written.
func containsJSON(body []byte, want string) bool {
	s := string(body)
	for i := 0; i+len(want) <= len(s); i++ {
		if s[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
