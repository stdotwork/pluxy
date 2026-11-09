package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// applyAll feeds events to one repairs, as relay does, and returns the last event's output, or the
// error of the event that failed.
func applyAll(events ...string) (string, error) {
	p := newRepairs()
	var out []byte
	for _, ev := range events {
		var err error
		if out, err = p.apply([]byte(ev)); err != nil {
			return "", err
		}
	}
	return string(out), nil
}

const (
	incompleteZero = `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","output":[],"usage":{"input_tokens":5,"output_tokens":0}}}`
	itemDone       = `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message"}}`
)

func incompleteWith(usage, output string) string {
	return `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","output":` + output + `,"usage":` + usage + `}}`
}

// A response.incomplete that produced nothing and reports exactly 0 output tokens is a silent
// abort: an error that is not retried and does not blame the account. Anything that shows output,
// or a usage that does not say exactly 0, leaves it a reply.
func TestEmptyIncompleteIsAnError(t *testing.T) {
	_, err := applyAll(incompleteZero)
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v, want an *llm.Error", err)
	}
	if e.Code != llm.CodeUpstream || e.Status != http.StatusBadGateway || e.Scope != llm.ScopeRequest || e.Retryable ||
		e.Message != "stream error: upstream terminated with incomplete empty response (0 tokens)" {
		t.Errorf("got %+v", e)
	}

	for _, c := range []struct {
		name   string
		events []string
	}{
		{"a text delta", []string{`{"type":"response.output_text.delta","output_index":0,"delta":"Hi"}`, incompleteZero}},
		{"a reasoning text delta", []string{`{"type":"response.reasoning_text.delta","output_index":0,"delta":"hm"}`, incompleteZero}},
		{"a reasoning summary delta", []string{`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"hm"}`, incompleteZero}},
		{"a tool argument delta", []string{`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{"}`, incompleteZero}},
		{"a finished item", []string{itemDone, incompleteZero}},
		{"a finished item without an output index", []string{`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message"}}`, incompleteZero}},
		{"output in the event", []string{incompleteWith(`{"output_tokens":0}`, `[{"id":"msg_1","type":"message"}]`)}},
		{"no usage", []string{incompleteWith(`null`, `[]`)}},
		{"no output_tokens", []string{incompleteWith(`{"input_tokens":5}`, `[]`)}},
		{"output_tokens 0.0", []string{incompleteWith(`{"output_tokens":0.0}`, `[]`)}},
		{"output_tokens a string", []string{incompleteWith(`{"output_tokens":"0"}`, `[]`)}},
		{"output_tokens null", []string{incompleteWith(`{"output_tokens":null}`, `[]`)}},
		{"output_tokens above 0", []string{incompleteWith(`{"output_tokens":3}`, `[]`)}},
		{"a completed reply", []string{strings.Replace(incompleteZero, "response.incomplete", "response.completed", 1)}},
		{"a response.done", []string{strings.Replace(incompleteZero, "response.incomplete", "response.done", 1)}},
	} {
		if _, err := applyAll(c.events...); err != nil {
			t.Errorf("%s: %v; want the event passed on", c.name, err)
		}
	}

	// Deltas with no text, or events that name no item, do not count as output.
	for _, events := range [][]string{
		{`{"type":"response.output_text.delta","output_index":0,"delta":"  "}`, incompleteZero},
		{`{"type":"response.output_text.delta","output_index":0}`, incompleteZero},
		{`{"type":"response.output_item.done","output_index":0}`, incompleteZero},
		{`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message"}}`, incompleteZero},
	} {
		if _, err := applyAll(events...); err == nil {
			t.Errorf("%v: passed, want the empty incomplete error", events)
		}
	}
}

// An output item of the terminal event with no id (missing, null or blank) gets the id of the
// finished item at the same position; every other item stays as it came.
func TestOutputItemIDsAreRestored(t *testing.T) {
	done := func(i int, item string) string {
		return `{"type":"response.output_item.done","output_index":` + string(rune('0'+i)) + `,"item":` + item + `}`
	}
	completed := func(output string) string {
		return `{"type":"response.completed","response":{"id":"r","status":"completed","output":` + output + `}}`
	}
	ids := func(t *testing.T, out string) []any {
		t.Helper()
		var ev struct {
			Response struct {
				Output []map[string]any `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(out), &ev); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		var got []any
		for _, it := range ev.Response.Output {
			got = append(got, it["id"])
		}
		return got
	}
	for _, c := range []struct {
		name   string
		events []string
		want   []any
	}{
		{"missing", []string{done(0, `{"id":"msg_1","type":"message"}`), completed(`[{"type":"message"}]`)}, []any{"msg_1"}},
		{"null", []string{done(0, `{"id":"msg_1","type":"message"}`), completed(`[{"id":null,"type":"message"}]`)}, []any{"msg_1"}},
		{"blank", []string{done(0, `{"id":"msg_1","type":"message"}`), completed(`[{"id":"  ","type":"message"}]`)}, []any{"msg_1"}},
		{"by position", []string{done(0, `{"id":"rs_1","type":"reasoning"}`), done(1, `{"id":"msg_1","type":"message"}`),
			completed(`[{"id":"rs_kept","type":"reasoning"},{"type":"message"}]`)}, []any{"rs_kept", "msg_1"}},
		{"in a response.done", []string{done(0, `{"id":"msg_1","type":"message"}`),
			strings.Replace(completed(`[{"type":"message"}]`), "response.completed", "response.done", 1)}, []any{"msg_1"}},
		{"an id it has stays", []string{done(0, `{"id":"msg_1","type":"message"}`), completed(`[{"id":"msg_own","type":"message"}]`)}, []any{"msg_own"}},
		{"a number id stays", []string{done(0, `{"id":"msg_1","type":"message"}`), completed(`[{"id":7,"type":"message"}]`)}, []any{float64(7)}},
		{"no finished item at the position", []string{done(1, `{"id":"msg_1","type":"message"}`), completed(`[{"type":"message"}]`)}, []any{nil}},
		{"the finished item has no id", []string{done(0, `{"type":"message"}`), completed(`[{"type":"message"}]`)}, []any{nil}},
		{"the finished item's id is blank", []string{done(0, `{"id":" ","type":"message"}`), completed(`[{"type":"message"}]`)}, []any{nil}},
		{"nothing finished", []string{completed(`[{"type":"message"}]`)}, []any{nil}},
	} {
		out, err := applyAll(c.events...)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := ids(t, out)
		if len(got) != len(c.want) {
			t.Errorf("%s: ids %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: ids %v, want %v", c.name, got, c.want)
				break
			}
		}
	}

	// The rest of an item is not touched, and an incomplete reply is restored like a completed one.
	out, err := applyAll(done(0, `{"id":"msg_1","type":"message"}`),
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"output_tokens":1}}}`)
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Response struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(out), &ev); err != nil || len(ev.Response.Output) != 1 {
		t.Fatalf("%s: %v", out, err)
	}
	var item map[string]json.RawMessage
	_ = json.Unmarshal(ev.Response.Output[0], &item)
	if string(item["id"]) != `"msg_1"` || string(item["status"]) != `"completed"` || !strings.Contains(string(item["content"]), `"text":"Hi"`) {
		t.Errorf("item %s", ev.Response.Output[0])
	}
}

// relay fails an empty incomplete reply with the error and sends no terminal event.
func TestRelayEmptyIncomplete(t *testing.T) {
	body := "data: " + `{"type":"response.created","response":{"id":"r","status":"in_progress","model":"gpt-test","output":[]}}` + "\n\n" +
		"data: " + incompleteZero + "\n\n"
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	out := &collected{}
	err := relay(context.Background(), resp, out)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeRequest || e.Retryable {
		t.Fatalf("relay: %v; want the empty incomplete error", err)
	}
	for _, ev := range out.events {
		if ev.Terminal() {
			t.Errorf("sent the terminal event %+v", ev)
		}
	}
}
