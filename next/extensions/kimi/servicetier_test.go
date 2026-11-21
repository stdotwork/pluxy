package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A request from another format goes to Kimi without the service tier and verbosity: nothing says
// Kimi's API takes them, so each is reported as dropped, at the client's field, as before they were
// carried across formats.
func TestServiceTierAndVerbosityNotSent(t *testing.T) {
	resp, err := openairesponses.ParseRequest([]byte(`{"model":"kimi-k3","service_tier":"priority","text":{"verbosity":"low"},"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := anthropicmessages.ParseRequest([]byte(`{"model":"kimi-k3","speed":"fast","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		req     llm.Request
		dropped []string
	}{
		{"responses", resp, []string{"service_tier", "text.verbosity"}},
		{"messages", msg, []string{"speed"}},
	} {
		body, tr, err := buildBody(c.req, upChat, nil)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"service_tier", "verbosity", "speed", "text"} {
			if _, ok := m[f]; ok {
				t.Errorf("%s: body has %s: %s", c.name, f, body)
			}
		}
		if d := tr.DroppedPaths(); !slices.Equal(d, c.dropped) {
			t.Errorf("%s: dropped %q, want %q", c.name, d, c.dropped)
		}
	}
}
