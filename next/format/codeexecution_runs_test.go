package format_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func lastRuns(evs []llm.Event) []llm.CodeExecution {
	return evs[len(evs)-1].CodeExecutions
}

// geminiPartKinds names the parts of a Gemini reply in order, for comparing two replies.
func geminiPartKinds(parts []map[string]any) []string {
	var out []string
	for _, p := range parts {
		switch {
		case p["executableCode"] != nil:
			out = append(out, "code")
		case p["codeExecutionResult"] != nil:
			out = append(out, "result")
		case p["functionCall"] != nil:
			out = append(out, "call")
		case p["text"] != nil:
			out = append(out, fmt.Sprintf("text:%v", p["text"]))
		}
	}
	return out
}

func aggParts(m map[string]any) []map[string]any {
	cands, _ := m["candidates"].([]any)
	c0, _ := cands[0].(map[string]any)
	content, _ := c0["content"].(map[string]any)
	var parts []map[string]any
	for _, p := range content["parts"].([]any) {
		parts = append(parts, p.(map[string]any))
	}
	return parts
}

// The runs carry no position, so a Gemini client gets them in one place whether it streams or not:
// after the text and before the calls that follow it.
func TestCodeRunsArePlacedAlikeStreamedAndNot(t *testing.T) {
	const head = `{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[
		{"type":"text","text":"BEFORE"},
		{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":{"code":"print(1)"}},
		{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"1\n","stderr":"","return_code":0,"content":[]}},
		{"type":"text","text":"AFTER"}`
	const tail = `],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	chunks := func(call bool) []string {
		c := []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"BEFORE"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"print(1)\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"1\n","stderr":"","return_code":0,"content":[]}}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"AFTER"}}`,
			`{"type":"content_block_stop","index":3}`,
		}
		if call {
			c = append(c,
				`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"toolu_1","name":"fn","input":{}}}`,
				`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":4}`)
		}
		return append(c,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`)
	}
	for _, tc := range []struct {
		name string
		call bool
		want []string
	}{
		{"text only", false, []string{"text:BEFORE", "text:AFTER", "code", "result"}},
		{"calls follow", true, []string{"text:BEFORE", "text:AFTER", "code", "result", "call"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := head + tail
			if tc.call {
				body = head + `,{"type":"tool_use","id":"toolu_1","name":"fn","input":{}}` + tail
			}
			evs, err := anthropicmessages.DecodeResponse([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			agg, _ := renderGeminiAgg(t, roundTripEvents(t, evs))
			if got := geminiPartKinds(aggParts(agg)); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("not streamed: %v, want %v", got, tc.want)
			}
			sse, _ := renderGeminiStream(t, decodeMessagesStream(t, chunks(tc.call)))
			if got := geminiPartKinds(collectGeminiParts(parseGeminiSSEChunks(t, sse))); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("streamed: %v, want %v", got, tc.want)
			}
		})
	}
}

// A run with no usable result is not carried and is not lost silently: every decoder ends the
// runs with the record that says runs were left out, and a client of another format hears of it.
func TestRunsWithNoUsableResultAreReportedLost(t *testing.T) {
	gemini := func(parts string) string {
		return `{"responseId":"g","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[` + parts + `,{"text":"hi"}]},"finishReason":"STOP"}]}`
	}
	const (
		geminiNoResult   = `{"executableCode":{"language":"PYTHON","code":"print(1)"}}`
		geminiUnspecific = `{"executableCode":{"language":"PYTHON","code":"print(1)"}},{"codeExecutionResult":{"outcome":"OUTCOME_UNSPECIFIED","output":"x"}}`
		msgHead          = `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
		msgTail          = `{"type":"message_delta","delta":{"stop_reason":"pause_turn","stop_sequence":null},"usage":{"output_tokens":2}}`
		msgCall          = `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"bash_code_execution","input":{"command":"ls"}}}`
		msgCallStop      = `{"type":"content_block_stop","index":0}`
	)
	msgDone := func(content string) string {
		return `{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[` + content + `],"stop_reason":"pause_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	}
	respDone := func(item string) string {
		return `{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra","output":[` + item + `]}`
	}
	respStream := func(item string) []string {
		return []string{
			`{"type":"response.created","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"status":"in_progress","model":"gpt-6.2-astra","output":[]}}`,
			`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":` + item + `}`,
			`{"type":"response.completed","sequence_number":3,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra","output":[` + item + `]}}`,
		}
	}
	const (
		interpreting = `{"id":"ci_1","type":"code_interpreter_call","status":"interpreting","container_id":"c","code":"x=1","outputs":null}`
		outputsNull  = `{"id":"ci_1","type":"code_interpreter_call","status":"completed","container_id":"c","code":"x=1","outputs":null}`
	)
	cases := []struct {
		name string
		evs  func(t *testing.T) []llm.Event
	}{
		{"gemini code without result", func(t *testing.T) []llm.Event {
			evs, err := geminigenerate.DecodeResponse([]byte(gemini(geminiNoResult)))
			if err != nil {
				t.Fatal(err)
			}
			return roundTripEvents(t, evs)
		}},
		{"gemini code without result, streamed", func(t *testing.T) []llm.Event {
			return decodeGeminiStream(t, []string{gemini(geminiNoResult)})
		}},
		{"gemini unspecified outcome", func(t *testing.T) []llm.Event {
			evs, err := geminigenerate.DecodeResponse([]byte(gemini(geminiUnspecific)))
			if err != nil {
				t.Fatal(err)
			}
			return roundTripEvents(t, evs)
		}},
		{"gemini unspecified outcome, streamed", func(t *testing.T) []llm.Event {
			return decodeGeminiStream(t, []string{gemini(geminiUnspecific)})
		}},
		{"responses still interpreting", func(t *testing.T) []llm.Event {
			evs, err := openairesponses.DecodeResponse([]byte(respDone(interpreting)))
			if err != nil {
				t.Fatal(err)
			}
			return roundTripEvents(t, evs)
		}},
		{"responses still interpreting, streamed", func(t *testing.T) []llm.Event {
			return decodeResponsesStream(t, respStream(interpreting))
		}},
		{"responses outputs null", func(t *testing.T) []llm.Event {
			evs, err := openairesponses.DecodeResponse([]byte(respDone(outputsNull)))
			if err != nil {
				t.Fatal(err)
			}
			return roundTripEvents(t, evs)
		}},
		{"responses outputs null, streamed", func(t *testing.T) []llm.Event {
			return decodeResponsesStream(t, respStream(outputsNull))
		}},
		{"messages call without result", func(t *testing.T) []llm.Event {
			evs, err := anthropicmessages.DecodeResponse([]byte(msgDone(`{"type":"server_tool_use","id":"srvtoolu_1","name":"bash_code_execution","input":{"command":"ls"}}`)))
			if err != nil {
				t.Fatal(err)
			}
			return roundTripEvents(t, evs)
		}},
		{"messages call without result, streamed", func(t *testing.T) []llm.Event {
			return decodeMessagesStream(t, []string{msgHead, msgCall, msgCallStop, msgTail, `{"type":"message_stop"}`})
		}},
		{"messages unreadable result", func(t *testing.T) []llm.Event {
			evs, err := anthropicmessages.DecodeResponse([]byte(msgDone(
				`{"type":"server_tool_use","id":"srvtoolu_1","name":"bash_code_execution","input":{"command":"ls"}},` +
					`{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"future_result"}}`)))
			if err != nil {
				t.Fatal(err)
			}
			return roundTripEvents(t, evs)
		}},
		{"messages unreadable result, streamed", func(t *testing.T) []llm.Event {
			return decodeMessagesStream(t, []string{msgHead, msgCall, msgCallStop,
				`{"type":"content_block_start","index":1,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"future_result"}}}`,
				`{"type":"content_block_stop","index":1}`, msgTail, `{"type":"message_stop"}`})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs := tc.evs(t)
			wantOnlyLeftOut(t, tc.name, lastRuns(evs))
			// Chat has no place for runs in any case, so its loss shows what the decoder said.
			_, losses := renderChatAggWithLosses(t, evs)
			if !hasLossPrefix(losses, "code_execution.files") {
				t.Errorf("a Chat client was not told runs were left out: %+v", losses)
			}
			if !strings.HasPrefix(tc.name, "gemini") {
				if _, gl := renderGeminiAgg(t, evs); !hasLossPrefix(gl, "code_execution.files") {
					t.Errorf("a Gemini client was not told runs were left out: %+v", gl)
				}
			}
		})
	}
}

// A run whose outputs the upstream did not give is not a run that printed nothing.
func TestResponsesRunWithOutputsNullIsNotASuccess(t *testing.T) {
	body := func(outputs string) string {
		return `{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra","output":[
			{"id":"ci_1","type":"code_interpreter_call","status":"completed","container_id":"c","code":"print(1)","outputs":` + outputs + `}]}`
	}
	evs, err := openairesponses.DecodeResponse([]byte(body("null")))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range lastRuns(evs) {
		if r.Code != "" {
			t.Errorf("a run with outputs null is carried as a result: %+v", r)
		}
	}
	// No logs is a result: the code ran and printed nothing.
	evs, err = openairesponses.DecodeResponse([]byte(body("[]")))
	if err != nil {
		t.Fatal(err)
	}
	if runs := lastRuns(evs); len(runs) != 1 || runs[0].Code != "print(1)" {
		t.Errorf("a run with empty outputs = %+v, want the run", runs)
	}
}

// Code the decoder had to cut is left out, never shown as the run's code.
func TestResponsesStreamedCodeOverTheLimitIsLeftOut(t *testing.T) {
	long := strings.Repeat("a", 600<<10)
	delta := func(s string) string {
		b, _ := json.Marshal(s)
		return `{"type":"response.code_interpreter_call_code.delta","sequence_number":2,"output_index":0,"item_id":"ci_1","delta":` + string(b) + `}`
	}
	item := `{"id":"ci_1","type":"code_interpreter_call","status":"completed","container_id":"c","code":null,"outputs":[{"type":"logs","logs":"ok\n"}]}`
	evs := decodeResponsesStream(t, []string{
		`{"type":"response.created","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"status":"in_progress","model":"gpt-6.2-astra","output":[]}}`,
		delta(long), delta(long),
		// A small delta after the one that did not fit must not be kept as if it followed the first.
		delta("tail"),
		`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":` + item + `}`,
		`{"type":"response.completed","sequence_number":4,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra","output":[` + item + `]}}`,
	})
	runs := lastRuns(evs)
	for _, r := range runs {
		if r.Code != "" {
			t.Errorf("cut code (%d bytes) shown as the run's code", len(r.Code))
		}
	}
	wantOnlyLeftOut(t, "over-long streamed code", runs)
}

// A reply's code over the limit is left out the same way streamed and not, and code under it is
// kept the same way, including code whose JSON text is longer than the code.
func TestMessagesCodeLimitIsTheSameStreamedAndNot(t *testing.T) {
	call := func(code string) (string, []string) {
		in, _ := json.Marshal(map[string]string{"code": code})
		whole := `{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[
			{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":` + string(in) + `},
			{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"ok","stderr":"","return_code":0,"content":[]}}],
			"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		chunks := []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":{}}}`,
		}
		for s := string(in); s != ""; {
			n := min(len(s), 64<<10)
			pj, _ := json.Marshal(s[:n])
			chunks = append(chunks, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(pj)+`}}`)
			s = s[n:]
		}
		chunks = append(chunks,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"ok","stderr":"","return_code":0,"content":[]}}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`)
		return whole, chunks
	}
	for _, tc := range []struct {
		name string
		code string
		kept bool
	}{
		{"over the limit", strings.Repeat("x", 2<<20), false},
		{"newlines whose JSON passes the limit", strings.Repeat("\n", 700<<10), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whole, chunks := call(tc.code)
			evs, err := anthropicmessages.DecodeResponse([]byte(whole))
			if err != nil {
				t.Fatal(err)
			}
			for mode, runs := range map[string][]llm.CodeExecution{"not streamed": lastRuns(evs), "streamed": lastRuns(decodeMessagesStream(t, chunks))} {
				if tc.kept {
					if len(runs) != 1 || runs[0].Code != tc.code {
						t.Errorf("%s: want the run kept, got %d runs", mode, len(runs))
					}
				} else {
					wantOnlyLeftOut(t, mode, runs)
				}
			}
		})
	}
}

// A result that cannot be read takes its own code, so the next result does not pair with it.
func TestGeminiResultPairsWithItsOwnCode(t *testing.T) {
	body := `{"responseId":"g","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[
		{"executableCode":{"language":"PYTHON","code":"A()"}},
		{"codeExecutionResult":{"outcome":"OUTCOME_UNSPECIFIED","output":"from A"}},
		{"executableCode":{"language":"PYTHON","code":"B()"}},
		{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"from B"}}]},"finishReason":"STOP"}]}`
	evs, err := geminigenerate.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	runs := lastRuns(evs)
	if len(runs) != 2 || runs[0].Code != "B()" || runs[0].Output != "from B" || runs[1] != (llm.CodeExecution{FilesOmitted: true}) {
		t.Errorf("runs = %+v, want B with its own output, then the left-out record", runs)
	}
}

// A result with no ID answers the one code that waits for it. A code that never got a result is
// left out, and a result is never shown beside code it cannot be shown to answer.
func TestGeminiResultWithNoIDDoesNotTakeAnotherCode(t *testing.T) {
	reply := func(parts string) []llm.Event {
		evs, err := geminigenerate.DecodeResponse([]byte(`{"responseId":"g","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[` + parts + `]},"finishReason":"STOP"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	code := func(c string) string { return `{"executableCode":{"language":"PYTHON","code":"` + c + `"}}` }
	result := func(o string) string { return `{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"` + o + `"}}` }

	// A has no result, B has its own: B keeps its output and A is left out.
	runs := lastRuns(reply(code("A()") + "," + code("B()") + "," + result("from B")))
	if len(runs) != 2 || runs[0].Code != "B()" || runs[0].Output != "from B" || runs[1] != (llm.CodeExecution{FilesOmitted: true}) {
		t.Errorf("A without result: runs = %+v, want B with its own output, then the left-out record", runs)
	}
	// Two codes, then two results: which answers which cannot be told, so neither is shown.
	runs = lastRuns(reply(code("A()") + "," + code("B()") + "," + result("from A") + "," + result("from B")))
	wantOnlyLeftOut(t, "two codes, two results", runs)
	// Codes that each have their result are paired as they come, also after a lost one.
	runs = lastRuns(reply(code("A()") + "," + code("B()") + "," + result("from B") + "," + code("C()") + "," + result("from C")))
	if len(runs) != 3 || runs[0].Code != "B()" || runs[1].Code != "C()" || runs[1].Output != "from C" || runs[2] != (llm.CodeExecution{FilesOmitted: true}) {
		t.Errorf("a pair after a lost code: runs = %+v", runs)
	}
}

// A Gemini upstream's SSE answer to a non-streaming request is rebuilt, and keeps its runs first
// as it did before runs were placed for other formats.
func TestRebuiltGeminiReplyKeepsItsRunsFirst(t *testing.T) {
	evs := decodeGeminiStream(t, []string{
		`{"responseId":"g","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"BEFORE"}]}}]}`,
		`{"responseId":"g","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}},{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1\n"}}]}}]}`,
		`{"responseId":"g","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"AFTER"}]},"finishReason":"STOP"}]}`,
	})
	agg, _ := renderGeminiAgg(t, evs)
	got := geminiPartKinds(aggParts(agg))
	if len(got) < 3 || got[0] != "code" || got[1] != "result" {
		t.Errorf("parts = %v, want the code and its result first", got)
	}
}

// An item whose ID is not a string is still a code call: it is left out with a loss, not passed over.
func TestResponsesCallWithNumericIDIsReportedLost(t *testing.T) {
	item := `{"id":123,"type":"code_interpreter_call","status":"completed","container_id":"c","code":"print(1)","outputs":[{"type":"logs","logs":"1\n"}]}`
	evs, err := openairesponses.DecodeResponse([]byte(`{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra","output":[` + item + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	wantOnlyLeftOut(t, "not streamed", lastRuns(evs))
	evs = decodeResponsesStream(t, []string{
		`{"type":"response.created","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"status":"in_progress","model":"gpt-6.2-astra","output":[]}}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":` + item + `}`,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra","output":[` + item + `]}}`,
	})
	wantOnlyLeftOut(t, "streamed", lastRuns(evs))
}

// The same result block twice delivers the run once and loses nothing.
func TestMessagesRepeatedResultLosesNothing(t *testing.T) {
	call := `{"type":"server_tool_use","id":"srvtoolu_1","name":"bash_code_execution","input":{"command":"ls"}}`
	res := `{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"bash_code_execution_result","stdout":"a","stderr":"","return_code":0,"content":[]}}`
	evs, err := anthropicmessages.DecodeResponse([]byte(`{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[` + call + `,` + res + `,` + res + `],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	check := func(mode string, runs []llm.CodeExecution) {
		if len(runs) != 1 || runs[0].Code != "ls" || runs[0].FilesOmitted {
			t.Errorf("%s: runs = %+v, want the one run and no left-out record", mode, runs)
		}
	}
	check("not streamed", lastRuns(evs))
	blockRes := func(i int) []string {
		return []string{
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"bash_code_execution_result","stdout":"a","stderr":"","return_code":0,"content":[]}}}`, i),
			fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i)}
	}
	chunks := []string{
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"bash_code_execution","input":{"command":"ls"}}}`,
		`{"type":"content_block_stop","index":0}`}
	chunks = append(append(chunks, blockRes(1)...), blockRes(2)...)
	chunks = append(chunks, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`, `{"type":"message_stop"}`)
	check("streamed", lastRuns(decodeMessagesStream(t, chunks)))
}
