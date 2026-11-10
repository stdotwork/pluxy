package main

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A client's own Messages body has its empty web search domain lists removed and its tool call
// IDs with characters Anthropic refuses replaced, the same ID the same way, while everything else
// stays: a non-empty list, a list on a tool that is not web search, valid IDs. The expected IDs
// are the prefix and the old ID in unpadded URL-safe base64, worked out outside Go; a valid ID
// that is already the new one pushes it to an underscore in front.
func TestFixBodyRepairsMessagesBody(t *testing.T) {
	in := `{"model":"claude-test","max_tokens":10,
		"tools":[
			{"type":"web_search_20250305","name":"web_search","allowed_domains":[],"blocked_domains":["a.example"]},
			{"type":"web_search_20260209","name":"web_search2","blocked_domains":[]},
			{"name":"f","input_schema":{"type":"object"},"allowed_domains":[]}
		],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call.1|x","name":"f","input":{}},
				{"type":"tool_use","id":"fc:2","name":"f","input":{}},
				{"type":"tool_use","id":"toolu_ok","name":"f","input":{}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call.1|x","content":"1"},
				{"type":"tool_result","tool_use_id":"fc:2","content":"2"},
				{"type":"tool_result","tool_use_id":"toolu_ok","content":"3"},
				{"type":"text","text":"cpa_tid_v1_ZmM6Mg is taken below"}
			]},
			{"role":"assistant","content":[{"type":"tool_use","id":"cpa_tid_v1_ZmM6Mg","name":"f","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"cpa_tid_v1_ZmM6Mg","content":"4"}]}
		]}`
	want := `{"model":"claude-test","max_tokens":10,
		"tools":[
			{"type":"web_search_20250305","name":"web_search","blocked_domains":["a.example"]},
			{"type":"web_search_20260209","name":"web_search2"},
			{"name":"f","input_schema":{"type":"object"},"allowed_domains":[]}
		],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"cpa_tid_v1_Y2FsbC4xfHg","name":"f","input":{}},
				{"type":"tool_use","id":"_cpa_tid_v1_ZmM6Mg","name":"f","input":{}},
				{"type":"tool_use","id":"toolu_ok","name":"f","input":{}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"cpa_tid_v1_Y2FsbC4xfHg","content":"1"},
				{"type":"tool_result","tool_use_id":"_cpa_tid_v1_ZmM6Mg","content":"2"},
				{"type":"tool_result","tool_use_id":"toolu_ok","content":"3"},
				{"type":"text","text":"cpa_tid_v1_ZmM6Mg is taken below"}
			]},
			{"role":"assistant","content":[{"type":"tool_use","id":"cpa_tid_v1_ZmM6Mg","name":"f","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"cpa_tid_v1_ZmM6Mg","content":"4"}]}
		]}`
	body, tr, err := buildBody(llm.Request{Model: "claude-test", Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(in)}})
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Passthrough {
		t.Error("a repaired Messages body is still the client's body")
	}
	if !jsonEqual(body, json.RawMessage(want)) {
		t.Errorf("body\n%s\nwant\n%s", body, want)
	}
}
