package main

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// callsByCandidate returns each candidate's tool call name and arguments from a recorded reply:
// candidate 0 from its item events, a later one from its candidate events.
func callsByCandidate(t *testing.T, events []llm.Event) map[int][2]string {
	t.Helper()
	got := map[int][2]string{}
	for _, ev := range events {
		switch {
		case ev.Type == llm.EventItemStart && ev.Item != nil && ev.Item.Kind == llm.ItemToolCall:
			c := got[0]
			c[0] = ev.Item.Name
			got[0] = c
		case ev.Type == llm.EventToolInputDelta:
			c := got[0]
			c[1] += ev.Delta
			got[0] = c
		case ev.Type == llm.EventCandidate && ev.Candidate.Kind == llm.CandidateToolCallStart:
			c := got[ev.Candidate.Index]
			c[0] = ev.Candidate.Name
			got[ev.Candidate.Index] = c
		case ev.Type == llm.EventCandidate && ev.Candidate.Kind == llm.CandidateToolInputDelta:
			c := got[ev.Candidate.Index]
			c[1] += ev.Candidate.Delta
			got[ev.Candidate.Index] = c
		}
	}
	return got
}

// Every candidate's tool call, not only the first, comes back under the client's tool name with
// an integer argument written as an integer, whether the reply streamed or not.
func TestReportingRepairsEveryCandidatesCall(t *testing.T) {
	req := llm.Request{Tools: []llm.Tool{{
		Name:       "lookup/item",
		Parameters: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}}}`),
	}}}
	tr := &llm.Translation{To: llm.FormatGeminiGenerate, From: llm.FormatOpenAIChat}
	cand := func(i int) string {
		return `{"index":` + string(rune('0'+i)) + `,"content":{"parts":[{"functionCall":{"name":"lookup_item","args":{"count":2.0}}}]},"finishReason":"STOP"}`
	}
	body := `{"candidates":[` + cand(0) + `,` + cand(1) + `,` + cand(2) + `]}`

	for _, mode := range []string{"stream", "whole"} {
		t.Run(mode, func(t *testing.T) {
			var events []llm.Event
			var err error
			if mode == "stream" {
				dec := geminigenerate.NewStreamDecoder()
				events, err = dec.Decode([]byte(body))
				if err == nil {
					var more []llm.Event
					more, err = dec.Close()
					events = append(events, more...)
				}
			} else {
				events, err = geminigenerate.DecodeResponse([]byte(body))
			}
			if err != nil {
				t.Fatal(err)
			}
			rec := &recorder{}
			r := &reporting{out: rec, tr: tr, names: replyNames(req, tr), schemas: toolSchemas(req), callNames: map[callKey]string{}}
			for _, ev := range events {
				if err := r.Send(ev); err != nil {
					t.Fatal(err)
				}
			}
			got := callsByCandidate(t, rec.events)
			for i := 0; i <= 2; i++ {
				if want := [2]string{"lookup/item", `{"count":2}`}; got[i] != want {
					t.Errorf("candidate %d call = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}
