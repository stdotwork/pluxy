package openaichat

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// chunk builds one stream chunk of choice 0 whose delta holds calls.
func chunk(delta string) string {
	return `{"id":"x","model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`
}

// decodeAll decodes the chunks into events, checking them against the protocol's ordering rules,
// and returns the decoder so its losses can be read.
func decodeAll(t *testing.T, chunks ...string) (*StreamDecoder, []llm.Event) {
	t.Helper()
	d := NewStreamDecoder()
	var out []llm.Event
	var seq llm.Sequence
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("%.60s: %v", c, err)
		}
		for _, ev := range evs {
			if err := seq.Check(ev); err != nil {
				t.Fatalf("%.60s: decoder broke the event rules: %v", c, err)
			}
			out = append(out, ev)
		}
	}
	return d, out
}

// eventLines summarises events as "type kind call_id delta" lines.
func eventLines(events []llm.Event) []string {
	var got []string
	for _, ev := range events {
		s := ev.Type
		if ev.Item != nil {
			s += " " + ev.Item.Kind + " " + ev.Item.CallID
		}
		if ev.Delta != "" {
			s += " " + ev.Delta
		}
		got = append(got, strings.TrimSpace(s))
	}
	return got
}

// A new id on an index already in use starts a new call, and a chunk with no index at all is a
// continuation only while it names no new id.
func TestStreamDecoderSplitsToolCallsByIndexAndID(t *testing.T) {
	_, events := decodeAll(t,
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"f","arguments":"{\"x\":"}}]}`),
		chunk(`{"tool_calls":[{"index":0,"id":"call_b","type":"function","function":{"name":"g","arguments":"{\"y\":"}}]}`),
		chunk(`{"tool_calls":[{"id":"call_c","type":"function","function":{"name":"h","arguments":"{\"z\":"}}]}`),
		chunk(`{"tool_calls":[{"function":{"arguments":"1}"}}]}`),
		chunk(`{"tool_calls":[{"id":"call_d","type":"function","function":{"name":"i","arguments":"{}"}}]}`),
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	want := []string{
		"response.start",
		"item.start tool_call call_a", `tool_input.delta {"x":`, "item.end",
		"item.start tool_call call_b", `tool_input.delta {"y":`, "item.end",
		"item.start tool_call call_c", `tool_input.delta {"z":`, "tool_input.delta 1}", "item.end",
		"item.start tool_call call_d", "tool_input.delta {}",
		"item.end",
	}
	if got := strings.Join(eventLines(events), "\n"); got != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// A call whose name only arrives in a later chunk keeps its arguments buffered until the item can
// start with the name. A call that never names its function is dropped and reported, never sent
// as an item.start core would refuse.
func TestStreamDecoderHoldsToolCallUntilName(t *testing.T) {
	d, events := decodeAll(t,
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"arguments":"{\"city\":"}}]}`),
		chunk(`{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"\"Oslo\"}"}}]}`),
		chunk(`{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"arguments":"{\"x\":1}"}}]}`),
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	want := []string{
		"response.start",
		"item.start tool_call call_a", `tool_input.delta {"city":`, `tool_input.delta "Oslo"}`, "item.end",
	}
	if got := strings.Join(eventLines(events), "\n"); got != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	l := d.Losses()
	if len(l) != 1 || l[0].Kind != llm.LossDropped || l[0].Path != "choices[0].delta.tool_calls[1]" {
		t.Errorf("losses = %+v, want one dropped call at choices[0].delta.tool_calls[1]", l)
	}
}

// A non-streamed reply whose call never names its function leaves the call out too: the same
// item.start core refuses.
func TestDecodeResponseLeavesOutNamelessToolCall(t *testing.T) {
	body := `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[
		{"id":"call_a","type":"function","function":{"arguments":"{}"}},
		{"id":"call_b","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	events, err := DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, ev := range events {
		if ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
			if ev.Item.Name == "" {
				t.Errorf("decoder emitted a tool call with no name")
			}
			calls = append(calls, ev.Item.CallID)
		}
	}
	if strings.Join(calls, ",") != "call_b" {
		t.Errorf("calls = %v, want only call_b", calls)
	}
}

// A stream whose call never names its function still produces one item.start per call that did.
func TestStreamDecoderNamelessOnlyCallHasNoItem(t *testing.T) {
	d, events := decodeAll(t,
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"arguments":"{}"}}]}`),
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	for _, ev := range events {
		if ev.Item != nil {
			t.Errorf("decoder emitted an item: %+v", ev.Item)
		}
	}
	if l := d.Losses(); len(l) != 1 || l[0].Path != "choices[0].delta.tool_calls[0]" {
		t.Errorf("losses = %+v, want one dropped call", l)
	}
}
