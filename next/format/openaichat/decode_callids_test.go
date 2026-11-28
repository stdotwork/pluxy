package openaichat

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestStreamDecoderFallbackCallIDs(t *testing.T) {
	_, events := decodeAll(t,
		chunk(`{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"f","arguments":"{}"}}]}`),
		chunk(`{"tool_calls":[{"index":1,"type":"function","function":{"name":"g","arguments":"{}"}}]}`),
		chunk(`{"tool_calls":[{"index":2,"id":"call_2","type":"function","function":{"name":"h","arguments":"{}"}}]}`),
		chunk(`{"tool_calls":[{"index":4,"id":"call_3","type":"function","function":{"name":"i","arguments":"{}"}}]}`),
		chunk(`{"tool_calls":[{"index":3,"type":"function","function":{"name":"j","arguments":"{}"}}]}`),
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	var got []string
	for _, ev := range events {
		if ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
			got = append(got, ev.Item.CallID)
		}
	}
	want := "call_0,call_1,call_2,call_3,call_4"
	if strings.Join(got, ",") != want {
		t.Errorf("call IDs = %v, want %s", got, want)
	}
}

func TestStreamDecoderFallbackCallIDWithoutIndex(t *testing.T) {
	_, events := decodeAll(t,
		chunk(`{"tool_calls":[{"type":"function","function":{"name":"f","arguments":"{}"}}]}`),
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	var got []string
	for _, ev := range events {
		if ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
			got = append(got, ev.Item.CallID)
		}
	}
	if strings.Join(got, ",") != "call_0" {
		t.Errorf("call IDs = %v, want call_0", got)
	}
}
