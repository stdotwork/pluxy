package llm

import (
	"strings"
	"testing"
)

func TestSequence(t *testing.T) {
	start := Event{Type: EventResponseStart, Response: &ResponseInfo{ID: "r", Model: "m"}}
	msg := func(i int) Event { return Event{Type: EventItemStart, Index: i, Item: &ItemStart{Kind: ItemMessage}} }
	tool := func(i int) Event {
		return Event{Type: EventItemStart, Index: i, Item: &ItemStart{Kind: ItemToolCall, CallID: "c", Name: "f"}}
	}
	text := func(i int) Event { return Event{Type: EventTextDelta, Index: i, Delta: "x"} }
	args := func(i int) Event { return Event{Type: EventToolInputDelta, Index: i, Delta: "{"} }
	end := func(i int) Event { return Event{Type: EventItemEnd, Index: i} }
	finish := Event{Type: EventFinish, Finish: &Finish{Reason: FinishStop}}
	fail := Event{Type: EventError, Error: &Error{Code: CodeUpstream}}

	cases := []struct {
		name    string
		events  []Event
		wantErr string // substring of the error on the last event; empty means all accepted
	}{
		{"text reply", []Event{start, msg(0), text(0), text(0), end(0), finish}, ""},
		{"interleaved tool calls", []Event{start, tool(0), tool(1), args(1), args(0), end(0), end(1), finish}, ""},
		{"error first", []Event{fail}, ""},
		{"error mid-stream", []Event{start, msg(0), text(0), fail}, ""},
		{"delta before start", []Event{text(0)}, "first event"},
		{"skipped index", []Event{start, msg(1)}, "index 1, want 0"},
		{"delta into closed item", []Event{start, msg(0), end(0), text(0)}, "not open"},
		{"wrong delta kind", []Event{start, msg(0), args(0)}, "into message item"},
		{"finish with open item", []Event{start, msg(0), finish}, "still open"},
		{"event after finish", []Event{start, finish, msg(0)}, "after the terminal"},
		{"tool call without id", []Event{start, {Type: EventItemStart, Item: &ItemStart{Kind: ItemToolCall, Name: "f"}}}, "without call_id"},
		{"error without code", []Event{{Type: EventError, Error: &Error{}}}, "without an error code"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s Sequence
			var err error
			for i, ev := range c.events {
				err = s.Check(ev)
				if err != nil && i != len(c.events)-1 {
					t.Fatalf("event %d (%s) rejected early: %v", i, ev.Type, err)
				}
			}
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, c.wantErr)
			}
		})
	}
}
