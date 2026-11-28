package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func translatedCall(format, args string) llm.Item {
	return llm.Item{Type: llm.ItemTypeToolCall, CallID: "call_1", Name: "get_weather", Arguments: args,
		Origin: &llm.Origin{Format: format, Path: "items[0]"}}
}

func encodeCallArgs(t *testing.T, it llm.Item) (string, []llm.Loss) {
	t.Helper()
	body, losses, err := EncodeRequest(llm.Request{Model: "m", Items: []llm.Item{it}})
	if err != nil {
		t.Fatalf("%+v: %v", it, err)
	}
	var out struct {
		Messages []struct {
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if len(out.Messages) != 1 || len(out.Messages[0].ToolCalls) != 1 {
		t.Fatalf("body %s has no single tool call", body)
	}
	return out.Messages[0].ToolCalls[0].Function.Arguments, losses
}

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

func TestEncodeRequestKeepsSameFormatToolArguments(t *testing.T) {
	for _, it := range []llm.Item{
		translatedCall(llm.FormatOpenAIChat, `{"city":`),
	} {
		got, losses := encodeCallArgs(t, it)
		if got != `{"city":` {
			t.Errorf("arguments became %q, want the bytes the client sent", got)
		}
		if len(losses) != 0 {
			t.Errorf("losses = %+v, want none", losses)
		}
	}
}

func TestEncodeRequestKeepsCustomToolInput(t *testing.T) {
	it := translatedCall(llm.FormatAnthropicMessages, "*** Begin Patch\n")
	it.Custom = true
	body, losses, err := EncodeRequest(llm.Request{Model: "m", Items: []llm.Item{it}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"input":"*** Begin Patch\n"`; !strings.Contains(string(body), want) {
		t.Errorf("body %s does not hold the custom input %s", body, want)
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}
