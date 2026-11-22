package format_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func renderChatAggWithLosses(t *testing.T, evs []llm.Event) (map[string]any, []llm.Loss) {
	t.Helper()
	a := openaichat.NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatalf("chat agg: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal chat agg: %v", err)
	}
	return m, a.Losses()
}

func renderChatStreamWithLosses(t *testing.T, evs []llm.Event) (string, []llm.Loss) {
	t.Helper()
	var buf bytes.Buffer
	w := openaichat.NewStreamWriter(&buf, false)
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatalf("chat stream write: %v", err)
		}
	}
	return buf.String(), w.Losses()
}

func hasLossPrefix(losses []llm.Loss, prefix string) bool {
	for _, l := range losses {
		if l.Path == prefix || strings.HasPrefix(l.Path, prefix+".") {
			return true
		}
	}
	return false
}

func parseGeminiSSEChunks(t *testing.T, sseText string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(sseText, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err != nil {
			t.Fatalf("unmarshal gemini SSE chunk %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func collectGeminiParts(chunks []map[string]any) []map[string]any {
	var parts []map[string]any
	for _, ch := range chunks {
		cands, _ := ch["candidates"].([]any)
		if len(cands) == 0 {
			continue
		}
		c0, _ := cands[0].(map[string]any)
		content, _ := c0["content"].(map[string]any)
		ps, _ := content["parts"].([]any)
		for _, p := range ps {
			if pm, ok := p.(map[string]any); ok {
				parts = append(parts, pm)
			}
		}
	}
	return parts
}

// Real upstream reply bodies for Messages, Responses, and Gemini.

const messagesBashReply = `{
  "id": "msg_ce_1",
  "type": "message",
  "role": "assistant",
  "model": "claude-sonnet-5",
  "content": [
    {
      "type": "server_tool_use",
      "id": "srvtoolu_01BashRun",
      "name": "bash_code_execution",
      "input": {"command": "python3 -c 'print(2+2)'"}
    },
    {
      "type": "bash_code_execution_tool_result",
      "tool_use_id": "srvtoolu_01BashRun",
      "content": {
        "type": "bash_code_execution_result",
        "stdout": "4\n",
        "stderr": "",
        "return_code": 0,
        "content": []
      }
    },
    {"type": "text", "text": "The answer is 4."}
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 10, "output_tokens": 20}
}`

const messagesLegacyPythonReply = `{
  "id": "msg_ce_py",
  "type": "message",
  "role": "assistant",
  "model": "claude-sonnet-5",
  "content": [
    {
      "type": "server_tool_use",
      "id": "srvtoolu_01PyRun",
      "name": "code_execution",
      "input": {"code": "print(3 * 7)"}
    },
    {
      "type": "code_execution_tool_result",
      "tool_use_id": "srvtoolu_01PyRun",
      "content": {
        "type": "code_execution_result",
        "stdout": "21\n",
        "stderr": "",
        "return_code": 0,
        "content": []
      }
    },
    {"type": "text", "text": "21."}
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 10, "output_tokens": 20}
}`

const messagesTextEditorReply = `{
  "id": "msg_ce_te",
  "type": "message",
  "role": "assistant",
  "model": "claude-sonnet-5",
  "content": [
    {
      "type": "server_tool_use",
      "id": "srvtoolu_01ViewRun",
      "name": "text_editor_code_execution",
      "input": {"command": "view", "path": "data.txt"}
    },
    {
      "type": "text_editor_code_execution_tool_result",
      "tool_use_id": "srvtoolu_01ViewRun",
      "content": {
        "type": "text_editor_code_execution_view_result",
        "file_type": "text",
        "content": "hello world",
        "num_lines": 1,
        "start_line": 1,
        "total_lines": 1
      }
    },
    {"type": "text", "text": "Read data.txt."}
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 10, "output_tokens": 20}
}`

const responsesCodeInterpreterReply = `{
  "id": "resp_ce_1",
  "object": "response",
  "created_at": 1700000000,
  "status": "completed",
  "model": "gpt-6.2-astra",
  "output": [
    {
      "id": "ci_12345",
      "type": "code_interpreter_call",
      "status": "completed",
      "container_id": "cntr_abc123",
      "code": "print(2 + 2)",
      "outputs": [
        {"type": "logs", "logs": "4\n"}
      ]
    },
    {
      "id": "msg_1",
      "type": "message",
      "status": "completed",
      "role": "assistant",
      "content": [{"type": "output_text", "text": "The answer is 4.", "annotations": []}]
    }
  ],
  "usage": {"input_tokens": 10, "output_tokens": 20, "total_tokens": 30}
}`

const geminiCodeExecReply = `{
  "responseId": "gem_ce_1",
  "modelVersion": "gemini-3.8-flash",
  "candidates": [
    {
      "index": 0,
      "content": {
        "role": "model",
        "parts": [
          {"executableCode": {"language": "PYTHON", "code": "print(2 + 2)"}},
          {"codeExecutionResult": {"outcome": "OUTCOME_OK", "output": "4\n"}},
          {"text": "The answer is 4."}
        ]
      },
      "finishReason": "STOP"
    }
  ],
  "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 20, "totalTokenCount": 30}
}`

func TestCodeExecutionCrossFormatMatrix(t *testing.T) {
	upstreams := []struct {
		name      string
		upFormat  string
		decodeAgg func(t *testing.T) []llm.Event
		decodeStr func(t *testing.T) []llm.Event
		wantCode  string
		wantOut   string
		wantText  string
	}{
		{
			name:     "messages-bash",
			upFormat: llm.FormatAnthropicMessages,
			decodeAgg: func(t *testing.T) []llm.Event {
				evs, err := anthropicmessages.DecodeResponse([]byte(messagesBashReply))
				if err != nil {
					t.Fatalf("decode messages: %v", err)
				}
				return roundTripEvents(t, evs)
			},
			decodeStr: func(t *testing.T) []llm.Event {
				return decodeMessagesStream(t, []string{
					`{"type":"message_start","message":{"id":"msg_ce_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01BashRun","name":"bash_code_execution","input":{}}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"python3 -c "}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"'print(2+2)'\"}"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"content_block_start","index":1,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_01BashRun","content":{"type":"bash_code_execution_result","stdout":"4\n","stderr":"","return_code":0,"content":[]}}}`,
					`{"type":"content_block_stop","index":1}`,
					`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"The answer is 4."}}`,
					`{"type":"content_block_stop","index":2}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`,
					`{"type":"message_stop"}`,
				})
			},
			wantCode: "python3 -c 'print(2+2)'",
			wantOut:  "4\n",
			wantText: "The answer is 4.",
		},
		{
			name:     "messages-legacy-python",
			upFormat: llm.FormatAnthropicMessages,
			decodeAgg: func(t *testing.T) []llm.Event {
				evs, err := anthropicmessages.DecodeResponse([]byte(messagesLegacyPythonReply))
				if err != nil {
					t.Fatalf("decode messages python: %v", err)
				}
				return roundTripEvents(t, evs)
			},
			decodeStr: func(t *testing.T) []llm.Event {
				return decodeMessagesStream(t, []string{
					`{"type":"message_start","message":{"id":"msg_ce_py","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01PyRun","name":"code_execution","input":{}}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"print(3 * 7)\"}"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"content_block_start","index":1,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_01PyRun","content":{"type":"code_execution_result","stdout":"21\n","stderr":"","return_code":0,"content":[]}}}`,
					`{"type":"content_block_stop","index":1}`,
					`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"21."}}`,
					`{"type":"content_block_stop","index":2}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`,
					`{"type":"message_stop"}`,
				})
			},
			wantCode: "print(3 * 7)",
			wantOut:  "21\n",
			wantText: "21.",
		},
		{
			name:     "messages-text-editor",
			upFormat: llm.FormatAnthropicMessages,
			decodeAgg: func(t *testing.T) []llm.Event {
				evs, err := anthropicmessages.DecodeResponse([]byte(messagesTextEditorReply))
				if err != nil {
					t.Fatalf("decode messages text_editor: %v", err)
				}
				return roundTripEvents(t, evs)
			},
			decodeStr: func(t *testing.T) []llm.Event {
				return decodeMessagesStream(t, []string{
					`{"type":"message_start","message":{"id":"msg_ce_te","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01ViewRun","name":"text_editor_code_execution","input":{}}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"view\",\"path\":\"data.txt\"}"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"content_block_start","index":1,"content_block":{"type":"text_editor_code_execution_tool_result","tool_use_id":"srvtoolu_01ViewRun","content":{"type":"text_editor_code_execution_view_result","file_type":"text","content":"hello world","num_lines":1,"start_line":1,"total_lines":1}}}`,
					`{"type":"content_block_stop","index":1}`,
					`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Read data.txt."}}`,
					`{"type":"content_block_stop","index":2}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`,
					`{"type":"message_stop"}`,
				})
			},
			wantCode: `{"command":"view","path":"data.txt"}`,
			wantOut:  "hello world",
			wantText: "Read data.txt.",
		},
		{
			name:     "responses",
			upFormat: llm.FormatOpenAIResponses,
			decodeAgg: func(t *testing.T) []llm.Event {
				evs, err := openairesponses.DecodeResponse([]byte(responsesCodeInterpreterReply))
				if err != nil {
					t.Fatalf("decode responses: %v", err)
				}
				return roundTripEvents(t, evs)
			},
			decodeStr: func(t *testing.T) []llm.Event {
				return decodeResponsesStream(t, []string{
					`{"type":"response.created","sequence_number":1,"response":{"id":"resp_ce_1","object":"response","created_at":1700000000,"status":"in_progress","model":"gpt-6.2-astra","output":[]}}`,
					`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"ci_12345","type":"code_interpreter_call","status":"in_progress","container_id":"cntr_abc123","code":"","outputs":null}}`,
					`{"type":"response.code_interpreter_call_code.delta","sequence_number":3,"output_index":0,"item_id":"ci_12345","delta":"print("}`,
					`{"type":"response.code_interpreter_call_code.delta","sequence_number":4,"output_index":0,"item_id":"ci_12345","delta":"2 + 2)"}`,
					`{"type":"response.code_interpreter_call_code.done","sequence_number":5,"output_index":0,"item_id":"ci_12345","code":"print(2 + 2)"}`,
					`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"ci_12345","type":"code_interpreter_call","status":"completed","container_id":"cntr_abc123","code":"print(2 + 2)","outputs":[{"type":"logs","logs":"4\n"}]}}`,
					`{"type":"response.output_item.added","sequence_number":7,"output_index":1,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
					`{"type":"response.output_text.delta","sequence_number":8,"output_index":1,"item_id":"msg_1","content_index":0,"delta":"The answer is 4."}`,
					`{"type":"response.output_item.done","sequence_number":9,"output_index":1,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"The answer is 4.","annotations":[]}]}}`,
					`{"type":"response.completed","sequence_number":10,"response":{"id":"resp_ce_1","object":"response","created_at":1700000000,"status":"completed","model":"gpt-6.2-astra","output":[{"id":"ci_12345","type":"code_interpreter_call","status":"completed","container_id":"cntr_abc123","code":"print(2 + 2)","outputs":[{"type":"logs","logs":"4\n"}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"The answer is 4.","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}`,
				})
			},
			wantCode: "print(2 + 2)",
			wantOut:  "4\n",
			wantText: "The answer is 4.",
		},
		{
			name:     "gemini",
			upFormat: llm.FormatGeminiGenerate,
			decodeAgg: func(t *testing.T) []llm.Event {
				evs, err := geminigenerate.DecodeResponse([]byte(geminiCodeExecReply))
				if err != nil {
					t.Fatalf("decode gemini: %v", err)
				}
				return roundTripEvents(t, evs)
			},
			decodeStr: func(t *testing.T) []llm.Event {
				return decodeGeminiStream(t, []string{
					`{"responseId":"gem_ce_1","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(2 + 2)"}}]}}]}`,
					`{"responseId":"gem_ce_1","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"4\n"}}]}}]}`,
					`{"responseId":"gem_ce_1","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"The answer is 4."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"totalTokenCount":30}}`,
				})
			},
			wantCode: "print(2 + 2)",
			wantOut:  "4\n",
			wantText: "The answer is 4.",
		},
	}

	for _, up := range upstreams {
		for _, mode := range []string{"nonstream", "stream"} {
			t.Run(up.name+"/"+mode, func(t *testing.T) {
				var evs []llm.Event
				if mode == "nonstream" {
					evs = up.decodeAgg(t)
				} else {
					evs = up.decodeStr(t)
				}
				fin := evs[len(evs)-1]
				if fin.Type != llm.EventFinish || len(fin.CodeExecutions) != 1 {
					t.Fatalf("finish event = %+v, want 1 code execution", fin)
				}
				ce := fin.CodeExecutions[0]
				if ce.Code != up.wantCode || ce.Output != up.wantOut || ce.Failed {
					t.Fatalf("decoded code execution = %+v, want code=%q output=%q failed=false", ce, up.wantCode, up.wantOut)
				}

				// 1. Gemini client (non-stream and stream)
				if up.upFormat != llm.FormatGeminiGenerate {
					gAgg, gAggLosses := renderGeminiAgg(t, evs)
					gParts := collectGeminiParts([]map[string]any{gAgg})
					gStr, gStrLosses := renderGeminiStream(t, evs)
					gStrParts := collectGeminiParts(parseGeminiSSEChunks(t, gStr))
					if up.name == "messages-bash" || up.name == "messages-text-editor" {
						// Gemini's code part holds only Python, so these runs are reported, not shown.
						for _, ps := range [][]map[string]any{gParts, gStrParts} {
							for _, p := range ps {
								if p["executableCode"] != nil || p["codeExecutionResult"] != nil {
									t.Errorf("non-Python run shown as a Gemini code part: %+v", p)
								}
							}
						}
						if !hasLossPrefix(gAggLosses, "code_execution") || !hasLossPrefix(gStrLosses, "code_execution") {
							t.Errorf("gemini losses = %+v and %+v, want code_execution", gAggLosses, gStrLosses)
						}
					} else {
						checkGeminiCodeParts(t, "agg", gParts, up.wantCode, "OUTCOME_OK", up.wantOut, up.wantText)
						checkGeminiCodeParts(t, "stream", gStrParts, up.wantCode, "OUTCOME_OK", up.wantOut, up.wantText)
					}
				}

				// 2. Messages client (cross-format: no structured block, text unchanged, loss reported)
				if up.upFormat != llm.FormatAnthropicMessages {
					mAgg, mAggLosses := renderMessagesAgg(t, evs)
					if !hasLossPrefix(mAggLosses, "content.code_execution") {
						t.Errorf("messages agg losses = %+v, want content.code_execution loss", mAggLosses)
					}
					var mObj map[string]any
					if err := json.Unmarshal(mAgg, &mObj); err != nil {
						t.Fatalf("unmarshal messages agg: %v", err)
					}
					content, _ := mObj["content"].([]any)
					if len(content) != 1 || content[0].(map[string]any)["text"] != up.wantText {
						t.Errorf("messages agg content = %+v, want only text %q", content, up.wantText)
					}

					_, mStrLosses := renderMessagesStream(t, evs)
					if !hasLossPrefix(mStrLosses, "content.code_execution") {
						t.Errorf("messages stream losses = %+v, want content.code_execution loss", mStrLosses)
					}
				}

				// 3. Responses client (cross-format: no structured block, text unchanged, loss reported)
				if up.upFormat != llm.FormatOpenAIResponses {
					rAgg, rAggLosses := renderResponsesAgg(t, evs)
					if !hasLossPrefix(rAggLosses, "code_execution") {
						t.Errorf("responses agg losses = %+v, want code_execution loss", rAggLosses)
					}
					output, _ := rAgg["output"].([]any)
					if len(output) != 1 || output[0].(map[string]any)["type"] != "message" {
						t.Errorf("responses agg output = %+v, want only message", output)
					}

					_, rStrLosses := renderResponsesStream(t, evs)
					if !hasLossPrefix(rStrLosses, "code_execution") {
						t.Errorf("responses stream losses = %+v, want code_execution loss", rStrLosses)
					}
				}

				// 4. Chat client (no structured block, text unchanged, loss reported)
				cAgg, cAggLosses := renderChatAggWithLosses(t, evs)
				if !hasLossPrefix(cAggLosses, "code_execution") {
					t.Errorf("chat agg losses = %+v, want code_execution loss", cAggLosses)
				}
				choices, _ := cAgg["choices"].([]any)
				msg := choices[0].(map[string]any)["message"].(map[string]any)
				if msg["content"] != up.wantText {
					t.Errorf("chat agg text = %q, want %q (must not alter model text)", msg["content"], up.wantText)
				}

				_, cStrLosses := renderChatStreamWithLosses(t, evs)
				if !hasLossPrefix(cStrLosses, "code_execution") {
					t.Errorf("chat stream losses = %+v, want code_execution loss", cStrLosses)
				}
			})
		}
	}
}

func checkGeminiCodeParts(t *testing.T, label string, parts []map[string]any, wantCode, wantOutcome, wantOut, wantText string) {
	t.Helper()
	var gotCode, gotLang, gotOutcome, gotOut, gotText string
	var hasCode, hasRes bool
	for _, p := range parts {
		if ec, ok := p["executableCode"].(map[string]any); ok {
			hasCode = true
			gotCode, _ = ec["code"].(string)
			gotLang, _ = ec["language"].(string)
		}
		if res, ok := p["codeExecutionResult"].(map[string]any); ok {
			hasRes = true
			gotOutcome, _ = res["outcome"].(string)
			gotOut, _ = res["output"].(string)
		}
		if txt, ok := p["text"].(string); ok {
			gotText += txt
		}
	}
	if !hasCode || gotCode != wantCode || gotLang != "PYTHON" {
		t.Errorf("gemini %s executableCode: has=%v lang=%q code=%q, want lang=PYTHON code=%q", label, hasCode, gotLang, gotCode, wantCode)
	}
	if !hasRes || gotOutcome != wantOutcome || gotOut != wantOut {
		t.Errorf("gemini %s codeExecutionResult: has=%v outcome=%q output=%q, want outcome=%q output=%q", label, hasRes, gotOutcome, gotOut, wantOutcome, wantOut)
	}
	if gotText != wantText {
		t.Errorf("gemini %s text = %q, want %q", label, gotText, wantText)
	}
}

func TestCodeExecutionFailedRuns(t *testing.T) {
	cases := []struct {
		name    string
		decode  func(t *testing.T) []llm.Event
		wantOut string
	}{
		{
			name: "messages-nonzero-exit",
			decode: func(t *testing.T) []llm.Event {
				body := `{
					"id":"m1","type":"message","role":"assistant","model":"claude-sonnet-5",
					"content":[
						{"type":"server_tool_use","id":"srvtoolu_f1","name":"bash_code_execution","input":{"command":"false"}},
						{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_f1","content":{"type":"bash_code_execution_result","stdout":"","stderr":"boom\n","return_code":1,"content":[]}},
						{"type":"text","text":"Failed."}
					],
					"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}
				}`
				evs, err := anthropicmessages.DecodeResponse([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				return roundTripEvents(t, evs)
			},
			wantOut: "boom\n",
		},
		{
			name: "messages-tool-result-error",
			decode: func(t *testing.T) []llm.Event {
				body := `{
					"id":"m2","type":"message","role":"assistant","model":"claude-sonnet-5",
					"content":[
						{"type":"server_tool_use","id":"srvtoolu_f2","name":"bash_code_execution","input":{"command":"sleep 999"}},
						{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_f2","content":{"type":"bash_code_execution_tool_result_error","error_code":"execution_time_exceeded"}},
						{"type":"text","text":"Timed out."}
					],
					"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}
				}`
				evs, err := anthropicmessages.DecodeResponse([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				return roundTripEvents(t, evs)
			},
			wantOut: "execution_time_exceeded",
		},
		{
			name: "responses-failed",
			decode: func(t *testing.T) []llm.Event {
				body := `{
					"id":"r1","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra",
					"output":[
						{"id":"ci_f1","type":"code_interpreter_call","status":"failed","container_id":"cntr_1","code":"1/0","outputs":[{"type":"logs","logs":"ZeroDivisionError"}]},
						{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Error."}]}
					]
				}`
				evs, err := openairesponses.DecodeResponse([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				return roundTripEvents(t, evs)
			},
			wantOut: "ZeroDivisionError",
		},
		{
			name: "gemini-deadline-exceeded",
			decode: func(t *testing.T) []llm.Event {
				body := `{
					"responseId":"g1","modelVersion":"gemini-3.8-flash",
					"candidates":[{"index":0,"content":{"role":"model","parts":[
						{"executableCode":{"language":"PYTHON","code":"while True: pass"}},
						{"codeExecutionResult":{"outcome":"OUTCOME_DEADLINE_EXCEEDED","output":"timeout"}},
						{"text":"Timed out."}
					]},"finishReason":"STOP"}]
				}`
				evs, err := geminigenerate.DecodeResponse([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				return roundTripEvents(t, evs)
			},
			wantOut: "timeout",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs := tc.decode(t)
			fin := evs[len(evs)-1]
			if len(fin.CodeExecutions) != 1 || !fin.CodeExecutions[0].Failed || fin.CodeExecutions[0].Output != tc.wantOut {
				t.Fatalf("got %+v, want failed=true output=%q", fin.CodeExecutions, tc.wantOut)
			}
			// Render a Python run to Gemini (for non-Gemini upstreams) and verify OUTCOME_FAILED.
			if evs[0].Response.Format != llm.FormatGeminiGenerate && fin.CodeExecutions[0].Language == "python" {
				gAgg, _ := renderGeminiAgg(t, evs)
				parts := collectGeminiParts([]map[string]any{gAgg})
				var outcome string
				for _, p := range parts {
					if res, ok := p["codeExecutionResult"].(map[string]any); ok {
						outcome, _ = res["outcome"].(string)
					}
				}
				if outcome != "OUTCOME_FAILED" {
					t.Errorf("gemini outcome = %q, want OUTCOME_FAILED", outcome)
				}
			}
		})
	}
}

func TestCodeExecutionOnlyReplyNoText(t *testing.T) {
	// Gemini reply holding ONLY executableCode + codeExecutionResult and NO text.
	geminiCodeOnly := `{
		"responseId": "gem_only",
		"modelVersion": "gemini-3.8-flash",
		"candidates": [{
			"index": 0,
			"content": {
				"role": "model",
				"parts": [
					{"executableCode": {"language": "PYTHON", "code": "print(42)"}},
					{"codeExecutionResult": {"outcome": "OUTCOME_OK", "output": "42\n"}}
				]
			},
			"finishReason": "STOP"
		}]
	}`
	evs, err := geminigenerate.DecodeResponse([]byte(geminiCodeOnly))
	if err != nil {
		t.Fatalf("decode gemini code-only: %v", err)
	}
	evs = roundTripEvents(t, evs)
	fin := evs[len(evs)-1]
	if len(fin.CodeExecutions) != 1 || fin.CodeExecutions[0].Output != "42\n" {
		t.Fatalf("code-only decoded = %+v", fin.CodeExecutions)
	}

	// Messages: valid empty content, stop_reason=end_turn, loss reported.
	mBody, mLosses := renderMessagesAgg(t, evs)
	if !hasLossPrefix(mLosses, "content.code_execution") {
		t.Errorf("messages losses = %+v, want content.code_execution", mLosses)
	}
	var mObj map[string]any
	if err := json.Unmarshal(mBody, &mObj); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	if mObj["stop_reason"] != "end_turn" {
		t.Errorf("messages stop_reason = %v, want end_turn", mObj["stop_reason"])
	}

	// Responses: valid completed response with empty output, loss reported.
	rObj, rLosses := renderResponsesAgg(t, evs)
	if !hasLossPrefix(rLosses, "code_execution") {
		t.Errorf("responses losses = %+v, want code_execution", rLosses)
	}
	if rObj["status"] != "completed" {
		t.Errorf("responses status = %v, want completed", rObj["status"])
	}

	// Chat: valid stop completion with empty content, loss reported.
	cObj, cLosses := renderChatAggWithLosses(t, evs)
	if !hasLossPrefix(cLosses, "code_execution") {
		t.Errorf("chat losses = %+v, want code_execution", cLosses)
	}
	choices := cObj["choices"].([]any)
	if choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Errorf("chat finish_reason = %v, want stop", choices[0].(map[string]any)["finish_reason"])
	}

	// Responses code-only -> Gemini client gets executableCode + codeExecutionResult and STOP.
	respCodeOnly := `{
		"id":"resp_only","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra",
		"output":[{"id":"ci_only","type":"code_interpreter_call","status":"completed","container_id":"cntr_1","code":"print(42)","outputs":[{"type":"logs","logs":"42\n"}]}]
	}`
	revs, err := openairesponses.DecodeResponse([]byte(respCodeOnly))
	if err != nil {
		t.Fatalf("decode responses code-only: %v", err)
	}
	revs = roundTripEvents(t, revs)
	gAgg, _ := renderGeminiAgg(t, revs)
	checkGeminiCodeParts(t, "code-only-agg", collectGeminiParts([]map[string]any{gAgg}), "print(42)", "OUTCOME_OK", "42\n", "")
	gStr, _ := renderGeminiStream(t, revs)
	checkGeminiCodeParts(t, "code-only-stream", collectGeminiParts(parseGeminiSSEChunks(t, gStr)), "print(42)", "OUTCOME_OK", "42\n", "")
}

func TestCodeExecutionMalformedAndUnmatchedDropped(t *testing.T) {
	// 1. Messages: result with no matching call + unknown result type -> dropped, text intact.
	msgBad := `{
		"id":"m_bad","type":"message","role":"assistant","model":"claude-sonnet-5",
		"content":[
			{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_missing","content":{"type":"bash_code_execution_result","stdout":"orphan","stderr":"","return_code":0,"content":[]}},
			{"type":"server_tool_use","id":"srvtoolu_unk","name":"bash_code_execution","input":{"command":"ls"}},
			{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_unk","content":{"type":"unknown_future_result"}},
			{"type":"text","text":"Still works."}
		],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}
	}`
	mevs, err := anthropicmessages.DecodeResponse([]byte(msgBad))
	if err != nil {
		t.Fatalf("messages bad: %v", err)
	}
	if len(mevs[len(mevs)-1].CodeExecutions) != 0 {
		t.Errorf("messages bad produced %+v, want 0", mevs[len(mevs)-1].CodeExecutions)
	}

	// 2. Responses: in_progress (partial) and malformed outputs -> dropped, text intact.
	respBad := `{
		"id":"r_bad","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra",
		"output":[
			{"id":"ci_partial","type":"code_interpreter_call","status":"in_progress","container_id":"cntr_1","code":"x=1","outputs":null},
			{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Still works."}]}
		]
	}`
	revs, err := openairesponses.DecodeResponse([]byte(respBad))
	if err != nil {
		t.Fatalf("responses bad: %v", err)
	}
	if len(revs[len(revs)-1].CodeExecutions) != 0 {
		t.Errorf("responses bad produced %+v, want 0", revs[len(revs)-1].CodeExecutions)
	}

	// 3. Gemini: result with no matching executableCode + unknown outcome -> dropped, text intact.
	gemBad := `{
		"responseId":"g_bad","modelVersion":"gemini-3.8-flash",
		"candidates":[{"index":0,"content":{"role":"model","parts":[
			{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"orphan"}},
			{"executableCode":{"language":"PYTHON","code":"print(1)"}},
			{"codeExecutionResult":{"outcome":"OUTCOME_UNSPECIFIED","output":"bad"}},
			{"text":"Still works."}
		]},"finishReason":"STOP"}]
	}`
	gevs, err := geminigenerate.DecodeResponse([]byte(gemBad))
	if err != nil {
		t.Fatalf("gemini bad: %v", err)
	}
	if len(gevs[len(gevs)-1].CodeExecutions) != 0 {
		t.Errorf("gemini bad produced %+v, want 0", gevs[len(gevs)-1].CodeExecutions)
	}
}

func TestCodeExecutionFilesAndImagesReportedAsLoss(t *testing.T) {
	// Messages with generated file_id in content[].
	msgWithFile := `{
		"id":"m_file","type":"message","role":"assistant","model":"claude-sonnet-5",
		"content":[
			{"type":"server_tool_use","id":"srvtoolu_file","name":"bash_code_execution","input":{"command":"python3 plot.py"}},
			{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_file","content":{"type":"bash_code_execution_result","stdout":"saved\n","stderr":"","return_code":0,"content":[{"type":"bash_code_execution_output","file_id":"file_01abc"}]}},
			{"type":"text","text":"Done."}
		],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}
	}`
	mevs, err := anthropicmessages.DecodeResponse([]byte(msgWithFile))
	if err != nil {
		t.Fatalf("decode msgWithFile: %v", err)
	}
	mevs = roundTripEvents(t, mevs)
	if len(mevs[len(mevs)-1].CodeExecutions) != 1 || !mevs[len(mevs)-1].CodeExecutions[0].FilesOmitted {
		t.Fatalf("expected FilesOmitted=true, got %+v", mevs[len(mevs)-1].CodeExecutions)
	}
	_, gLosses := renderGeminiAgg(t, mevs)
	if !hasLossPrefix(gLosses, "code_execution.files") {
		t.Errorf("gemini agg losses = %+v, want code_execution.files", gLosses)
	}
	_, gStrLosses := renderGeminiStream(t, mevs)
	if !hasLossPrefix(gStrLosses, "code_execution.files") {
		t.Errorf("gemini stream losses = %+v, want code_execution.files", gStrLosses)
	}

	// Responses with image output in code_interpreter_call.outputs[].
	respWithImg := `{
		"id":"r_img","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra",
		"output":[
			{"id":"ci_img","type":"code_interpreter_call","status":"completed","container_id":"cntr_1","code":"plot()","outputs":[{"type":"logs","logs":"ok"},{"type":"image","url":"https://example.com/plot.png"}]},
			{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Done."}]}
		]
	}`
	revs, err := openairesponses.DecodeResponse([]byte(respWithImg))
	if err != nil {
		t.Fatalf("decode respWithImg: %v", err)
	}
	revs = roundTripEvents(t, revs)
	if len(revs[len(revs)-1].CodeExecutions) != 1 || !revs[len(revs)-1].CodeExecutions[0].FilesOmitted {
		t.Fatalf("expected FilesOmitted=true, got %+v", revs[len(revs)-1].CodeExecutions)
	}
	_, gLosses2 := renderGeminiAgg(t, revs)
	if !hasLossPrefix(gLosses2, "code_execution.files") {
		t.Errorf("gemini agg losses from responses = %+v, want code_execution.files", gLosses2)
	}
}

func compactBody(t *testing.T, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		t.Fatalf("compact json: %v", err)
	}
	return buf.String()
}

func TestCodeExecutionSameFormatByteIdentical(t *testing.T) {
	// 1. Messages non-stream & stream.
	msgWire := compactBody(t, messagesBashReply)
	mevs, err := anthropicmessages.DecodeResponse([]byte(msgWire))
	if err != nil {
		t.Fatal(err)
	}
	mBody, mLosses := renderMessagesAgg(t, roundTripEvents(t, mevs))
	if string(mBody) != msgWire {
		t.Errorf("messages non-stream changed:\ngot:  %s\nwant: %s", mBody, msgWire)
	}
	if len(mLosses) != 0 {
		t.Errorf("messages same-format losses = %+v, want none", mLosses)
	}

	msgStreamChunks := []string{
		`{"type":"message_start","message":{"id":"msg_s1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"bash_code_execution","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo hi\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"bash_code_execution_result","stdout":"hi\n","stderr":"","return_code":0,"content":[]}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	}
	mStr, mStrLosses := renderMessagesStream(t, decodeMessagesStream(t, msgStreamChunks))
	var wantMsgSSE strings.Builder
	for _, c := range msgStreamChunks {
		var hdr struct{ Type string }
		_ = json.Unmarshal([]byte(c), &hdr)
		wantMsgSSE.WriteString("event: " + hdr.Type + "\ndata: " + c + "\n\n")
	}
	if mStr != wantMsgSSE.String() {
		t.Errorf("messages stream changed:\ngot:\n%s\nwant:\n%s", mStr, wantMsgSSE.String())
	}
	if len(mStrLosses) != 0 {
		t.Errorf("messages stream same-format losses = %+v, want none", mStrLosses)
	}

	// 2. Responses non-stream & stream.
	respWire := compactBody(t, responsesCodeInterpreterReply)
	revs, err := openairesponses.DecodeResponse([]byte(respWire))
	if err != nil {
		t.Fatal(err)
	}
	ra := openairesponses.NewAggregator(llm.Request{Model: "gpt-6.2-astra"})
	for _, ev := range roundTripEvents(t, revs) {
		ra.Add(ev)
	}
	rBody, err := ra.Response()
	if err != nil {
		t.Fatal(err)
	}
	if string(rBody) != respWire {
		t.Errorf("responses non-stream changed:\ngot:  %s\nwant: %s", rBody, respWire)
	}
	if len(ra.Losses()) != 0 {
		t.Errorf("responses same-format losses = %+v, want none", ra.Losses())
	}

	respStreamChunks := []string{
		`{"type":"response.created","sequence_number":1,"response":{"id":"resp_s1","object":"response","created_at":1,"status":"in_progress","model":"gpt-6.2-astra","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"ci_1","type":"code_interpreter_call","status":"in_progress","container_id":"cntr_1","code":"","outputs":null}}`,
		`{"type":"response.code_interpreter_call_code.delta","sequence_number":3,"output_index":0,"item_id":"ci_1","delta":"print(1)"}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"ci_1","type":"code_interpreter_call","status":"completed","container_id":"cntr_1","code":"print(1)","outputs":[{"type":"logs","logs":"1\n"}]}}`,
		`{"type":"response.completed","sequence_number":5,"response":{"id":"resp_s1","object":"response","created_at":1,"status":"completed","model":"gpt-6.2-astra","output":[{"id":"ci_1","type":"code_interpreter_call","status":"completed","container_id":"cntr_1","code":"print(1)","outputs":[{"type":"logs","logs":"1\n"}]}]}}`,
	}
	rStr, rStrLosses := renderResponsesStream(t, decodeResponsesStream(t, respStreamChunks))
	var wantRespSSE strings.Builder
	for _, c := range respStreamChunks {
		var hdr struct{ Type string }
		_ = json.Unmarshal([]byte(c), &hdr)
		wantRespSSE.WriteString("event: " + hdr.Type + "\ndata: " + c + "\n\n")
	}
	if rStr != wantRespSSE.String() {
		t.Errorf("responses stream changed:\ngot:\n%s\nwant:\n%s", rStr, wantRespSSE.String())
	}
	if len(rStrLosses) != 0 {
		t.Errorf("responses stream same-format losses = %+v, want none", rStrLosses)
	}

	// 3. Gemini non-stream & stream.
	gemWire := compactBody(t, geminiCodeExecReply)
	gevs, err := geminigenerate.DecodeResponse([]byte(gemWire))
	if err != nil {
		t.Fatal(err)
	}
	ga := geminigenerate.NewAggregator()
	for _, ev := range roundTripEvents(t, gevs) {
		ga.Add(ev)
	}
	gBody, err := ga.Response()
	if err != nil {
		t.Fatal(err)
	}
	if string(gBody) != gemWire {
		t.Errorf("gemini non-stream changed:\ngot:  %s\nwant: %s", gBody, gemWire)
	}
	if len(ga.Losses()) != 0 {
		t.Errorf("gemini same-format losses = %+v, want none", ga.Losses())
	}

	gemStreamChunks := []string{
		`{"responseId":"g_s1","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]}}]}`,
		`{"responseId":"g_s1","modelVersion":"gemini-3.8-flash","candidates":[{"index":0,"content":{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1\n"}}]},"finishReason":"STOP"}]}`,
	}
	gStr, gStrLosses := renderGeminiStream(t, decodeGeminiStream(t, gemStreamChunks))
	var wantGemSSE strings.Builder
	for _, c := range gemStreamChunks {
		wantGemSSE.WriteString("data: " + c + "\n\n")
	}
	if gStr != wantGemSSE.String() {
		t.Errorf("gemini stream changed:\ngot:\n%s\nwant:\n%s", gStr, wantGemSSE.String())
	}
	if len(gStrLosses) != 0 {
		t.Errorf("gemini stream same-format losses = %+v, want none", gStrLosses)
	}
}

func TestCodeExecutionNextTurnReplay(t *testing.T) {
	// Take a Responses code_interpreter_call reply rendered for a Gemini client, then replay that
	// model turn in a next-turn Gemini request through ParseRequest and into every target encoder.
	evs, err := openairesponses.DecodeResponse([]byte(responsesCodeInterpreterReply))
	if err != nil {
		t.Fatal(err)
	}
	gAgg, _ := renderGeminiAgg(t, roundTripEvents(t, evs))
	modelContent := gAgg["candidates"].([]any)[0].(map[string]any)["content"]

	nextTurnReq, err := json.Marshal(map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Compute 2+2"}}},
			modelContent,
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Now double it"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := geminigenerate.ParseRequest("gemini-3.8-flash", false, nextTurnReq)
	if err != nil {
		t.Fatalf("ParseRequest on replayed Gemini reply: %v", err)
	}

	// Target 1: Gemini encoder preserves executableCode and codeExecutionResult.
	gBody, _, err := geminigenerate.EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest -> gemini: %v", err)
	}
	if !strings.Contains(string(gBody), `"executableCode"`) || !strings.Contains(string(gBody), `"codeExecutionResult"`) {
		t.Errorf("gemini replay missing code parts: %s", gBody)
	}

	// Target 2: Messages encoder drops foreign native parts with losses and succeeds.
	mBody, mLosses, err := anthropicmessages.EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest -> messages: %v", err)
	}
	if len(mLosses) == 0 {
		t.Errorf("messages replay expected losses for foreign Gemini code parts")
	}
	if strings.Contains(string(mBody), "executableCode") || strings.Contains(string(mBody), "srvtoolu_") {
		t.Errorf("messages replay must not send foreign code parts or invented IDs: %s", mBody)
	}

	// Target 3: Responses encoder drops foreign native parts with losses and succeeds.
	rBody, rLosses, err := openairesponses.EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest -> responses: %v", err)
	}
	if len(rLosses) == 0 {
		t.Errorf("responses replay expected losses for foreign Gemini code parts")
	}
	if strings.Contains(string(rBody), "executableCode") || strings.Contains(string(rBody), "code_interpreter_call") {
		t.Errorf("responses replay must not send foreign code parts: %s", rBody)
	}

	// Target 4: Chat encoder drops foreign native parts with losses and succeeds.
	cBody, cLosses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest -> chat: %v", err)
	}
	if len(cLosses) == 0 {
		t.Errorf("chat replay expected losses for foreign Gemini code parts")
	}
	if strings.Contains(string(cBody), "executableCode") {
		t.Errorf("chat replay must not send foreign code parts: %s", cBody)
	}
}

func TestCodeExecutionAggregatorSizeAccounting(t *testing.T) {
	bigOut := strings.Repeat("x", 10000)
	evs := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m", Format: llm.FormatOpenAIResponses}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, CodeExecutions: llm.CodeExecutions{
			{Code: "print('x'*10000)", Language: "python", Output: bigOut},
		}},
	}
	ga := geminigenerate.NewAggregator()
	for _, ev := range evs {
		ga.Add(ev)
	}
	if ga.Size() < len(bigOut) {
		t.Errorf("geminigenerate Aggregator.Size() = %d, want >= %d", ga.Size(), len(bigOut))
	}
}
