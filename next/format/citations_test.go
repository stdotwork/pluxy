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

func roundTripEvents(t *testing.T, evs []llm.Event) []llm.Event {
	t.Helper()
	b, err := json.Marshal(evs)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	var out []llm.Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal events: %v", err)
	}
	return out
}

func decodeMessagesStream(t *testing.T, chunks []string) []llm.Event {
	t.Helper()
	d := anthropicmessages.NewStreamDecoder()
	var evs []llm.Event
	for _, c := range chunks {
		e, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("messages stream decode %s: %v", c, err)
		}
		evs = append(evs, e...)
	}
	e, err := d.Close()
	if err != nil {
		t.Fatalf("messages stream close: %v", err)
	}
	return roundTripEvents(t, append(evs, e...))
}

func decodeResponsesStream(t *testing.T, chunks []string) []llm.Event {
	t.Helper()
	d := openairesponses.NewStreamDecoder()
	var evs []llm.Event
	for _, c := range chunks {
		e, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("responses stream decode %s: %v", c, err)
		}
		evs = append(evs, e...)
	}
	e, err := d.Close()
	if err != nil {
		t.Fatalf("responses stream close: %v", err)
	}
	return roundTripEvents(t, append(evs, e...))
}

func decodeGeminiStream(t *testing.T, chunks []string) []llm.Event {
	t.Helper()
	d := geminigenerate.NewStreamDecoder()
	var evs []llm.Event
	for _, c := range chunks {
		e, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("gemini stream decode %s: %v", c, err)
		}
		evs = append(evs, e...)
	}
	e, err := d.Close()
	if err != nil {
		t.Fatalf("gemini stream close: %v", err)
	}
	return roundTripEvents(t, append(evs, e...))
}

func decodeChatStream(t *testing.T, chunks []string) []llm.Event {
	t.Helper()
	d := openaichat.NewStreamDecoder()
	var evs []llm.Event
	for _, c := range chunks {
		e, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("chat stream decode %s: %v", c, err)
		}
		evs = append(evs, e...)
	}
	e, err := d.Close()
	if err != nil {
		t.Fatalf("chat stream close: %v", err)
	}
	return roundTripEvents(t, append(evs, e...))
}

func renderResponsesAgg(t *testing.T, evs []llm.Event) (map[string]any, []llm.Loss) {
	t.Helper()
	a := openairesponses.NewAggregator(llm.Request{Model: "m"})
	for _, ev := range evs {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatalf("responses agg: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal responses agg: %v", err)
	}
	return m, a.Losses()
}

func renderResponsesStream(t *testing.T, evs []llm.Event) (string, []llm.Loss) {
	t.Helper()
	var buf bytes.Buffer
	w := openairesponses.NewStreamWriter(&buf, llm.Request{Model: "m"})
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatalf("responses stream write: %v", err)
		}
	}
	return buf.String(), w.Losses()
}

func renderChatAgg(t *testing.T, evs []llm.Event) map[string]any {
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
	return m
}

func renderGeminiAgg(t *testing.T, evs []llm.Event) (map[string]any, []llm.Loss) {
	t.Helper()
	a := geminigenerate.NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatalf("gemini agg: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal gemini agg: %v", err)
	}
	return m, a.Losses()
}

func renderGeminiStream(t *testing.T, evs []llm.Event) (string, []llm.Loss) {
	t.Helper()
	var buf bytes.Buffer
	w := geminigenerate.NewStreamWriter(&buf)
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatalf("gemini stream write: %v", err)
		}
	}
	return buf.String(), w.Losses()
}

func renderMessagesAgg(t *testing.T, evs []llm.Event) ([]byte, []llm.Loss) {
	t.Helper()
	a := anthropicmessages.NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	body, err := a.Response()
	if err != nil {
		t.Fatalf("messages agg: %v", err)
	}
	return body, a.Losses()
}

func renderMessagesStream(t *testing.T, evs []llm.Event) (string, []llm.Loss) {
	t.Helper()
	var buf bytes.Buffer
	w := anthropicmessages.NewStreamWriter(&buf)
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatalf("messages stream write: %v", err)
		}
	}
	return buf.String(), w.Losses()
}

func TestGeminiToAllFormatsNonASCII(t *testing.T) {
	geminiBody := []byte(`{
		"responseId": "resp_g1",
		"modelVersion": "gemini-2.5-pro",
		"candidates": [{
			"index": 0,
			"content": {"role": "model", "parts": [{"text": "Café 東京 🚀 info"}]},
			"finishReason": "STOP",
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://vertexaisearch.cloud.google.com/grounding-api-redirect/abc", "title": "Tokyo Guide"}},
					{"web": {"uri": "https://example.com/extra", "title": "Extra Source"}}
				],
				"groundingSupports": [{
					"segment": {"startIndex": 6, "endIndex": 17, "text": "東京 🚀"},
					"groundingChunkIndices": [0]
				}]
			}
		}]
	}`)

	nonStreamEvs, err := geminigenerate.DecodeResponse(geminiBody)
	if err != nil {
		t.Fatalf("gemini DecodeResponse: %v", err)
	}
	nonStreamEvs = roundTripEvents(t, nonStreamEvs)

	streamEvs := decodeGeminiStream(t, []string{
		`{"responseId":"resp_g1","modelVersion":"gemini-2.5-pro","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Café "}]}}]}`,
		`{"responseId":"resp_g1","modelVersion":"gemini-2.5-pro","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"東京 🚀 info"}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/abc","title":"Tokyo Guide"}},{"web":{"uri":"https://example.com/extra","title":"Extra Source"}}],"groundingSupports":[{"segment":{"startIndex":6,"endIndex":17,"text":"東京 🚀"},"groundingChunkIndices":[0]}]}}]}`,
	})

	for _, mode := range []struct {
		name string
		evs  []llm.Event
	}{
		{"nonstream", nonStreamEvs},
		{"stream", streamEvs},
	} {
		t.Run(mode.name, func(t *testing.T) {
			respAgg, _ := renderResponsesAgg(t, mode.evs)
			out := respAgg["output"].([]any)
			msg := out[0].(map[string]any)
			content := msg["content"].([]any)[0].(map[string]any)
			anns := content["annotations"].([]any)
			if len(anns) != 1 {
				t.Fatalf("responses agg annotations = %v, want 1", anns)
			}
			ann := anns[0].(map[string]any)
			if ann["type"] != "url_citation" ||
				ann["url"] != "https://vertexaisearch.cloud.google.com/grounding-api-redirect/abc" ||
				ann["title"] != "Tokyo Guide" ||
				int(ann["start_index"].(float64)) != 5 || int(ann["end_index"].(float64)) != 9 {
				t.Errorf("responses agg annotation = %+v, want start=5 end=9", ann)
			}

			respSSE, _ := renderResponsesStream(t, mode.evs)
			if !strings.Contains(respSSE, "event: response.output_text.annotation.added") {
				t.Errorf("responses stream missing annotation.added:\n%s", respSSE)
			}
			if !strings.Contains(respSSE, `"start_index":5`) || !strings.Contains(respSSE, `"end_index":9`) {
				t.Errorf("responses stream wrong rune offsets:\n%s", respSSE)
			}

			chatAgg := renderChatAgg(t, mode.evs)
			chMsg := chatAgg["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
			chatAnns, ok := chMsg["annotations"].([]any)
			if !ok || len(chatAnns) != 1 {
				t.Fatalf("chat agg annotations = %v, want 1", chMsg["annotations"])
			}
			uc := chatAnns[0].(map[string]any)["url_citation"].(map[string]any)
			if uc["url"] != "https://vertexaisearch.cloud.google.com/grounding-api-redirect/abc" ||
				int(uc["start_index"].(float64)) != 5 || int(uc["end_index"].(float64)) != 9 {
				t.Errorf("chat agg url_citation = %+v, want start=5 end=9", uc)
			}

			msgBody, msgLosses := renderMessagesAgg(t, mode.evs)
			if strings.Contains(string(msgBody), "citations") || strings.Contains(string(msgBody), "web_search_result_location") {
				t.Errorf("messages agg unexpectedly emitted citations: %s", msgBody)
			}
			if len(msgLosses) == 0 {
				t.Errorf("messages agg reported no losses for dropped citations")
			}
			msgSSE, msgStreamLosses := renderMessagesStream(t, mode.evs)
			if strings.Contains(msgSSE, "citations_delta") {
				t.Errorf("messages stream unexpectedly emitted citations_delta: %s", msgSSE)
			}
			if len(msgStreamLosses) == 0 {
				t.Errorf("messages stream reported no losses for dropped citations")
			}
		})
	}

	directEvs, err := geminigenerate.DecodeResponse(geminiBody)
	if err != nil {
		t.Fatalf("direct gemini decode: %v", err)
	}
	a := geminigenerate.NewAggregator()
	for _, ev := range directEvs {
		a.Add(ev)
	}
	gotSame, err := a.Response()
	if err != nil {
		t.Fatalf("same-format gemini: %v", err)
	}
	if !bytes.Equal(gotSame, geminiBody) {
		t.Errorf("same-format gemini changed bytes:\ngot:  %s\nwant: %s", gotSame, geminiBody)
	}
}

func TestMessagesToResponsesChatGemini(t *testing.T) {
	msgBody := []byte(`{
		"id": "msg_1",
		"type": "message",
		"role": "assistant",
		"model": "claude-opus-4-6",
		"content": [
			{"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": {"query": "oslo"}},
			{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_1", "content": [
				{"type": "web_search_result", "url": "https://example.com/oslo", "title": "Oslo Guide", "encrypted_content": "ENC1"}
			]},
			{"type": "text", "text": "Intro: "},
			{"type": "text", "text": "Café 🚀", "citations": [
				{"type": "web_search_result_location", "url": "https://example.com/oslo", "title": "Oslo Guide", "encrypted_index": "IDX1", "cited_text": "Café info"}
			]}
		],
		"stop_reason": "end_turn",
		"stop_sequence": null,
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	nonStreamEvs, err := anthropicmessages.DecodeResponse(msgBody)
	if err != nil {
		t.Fatalf("messages DecodeResponse: %v", err)
	}
	nonStreamEvs = roundTripEvents(t, nonStreamEvs)

	streamEvs := decodeMessagesStream(t, []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"oslo\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://example.com/oslo","title":"Oslo Guide","encrypted_content":"ENC1"}]}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Intro: "}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Café 🚀"}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.com/oslo","title":"Oslo Guide","encrypted_index":"IDX1","cited_text":"Café info"}}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	})

	for _, mode := range []struct {
		name string
		evs  []llm.Event
	}{
		{"nonstream", nonStreamEvs},
		{"stream", streamEvs},
	} {
		t.Run(mode.name, func(t *testing.T) {
			respAgg, _ := renderResponsesAgg(t, mode.evs)
			out := respAgg["output"].([]any)
			var foundAnn map[string]any
			for _, raw := range out {
				it := raw.(map[string]any)
				if it["type"] == "message" {
					c := it["content"].([]any)[0].(map[string]any)
					if anns := c["annotations"].([]any); len(anns) > 0 {
						foundAnn = anns[0].(map[string]any)
					}
				}
			}
			if foundAnn == nil || foundAnn["url"] != "https://example.com/oslo" ||
				int(foundAnn["start_index"].(float64)) != 0 || int(foundAnn["end_index"].(float64)) != 6 {
				t.Errorf("responses agg annotation = %+v, want [0, 6)", foundAnn)
			}

			respSSE, _ := renderResponsesStream(t, mode.evs)
			if !strings.Contains(respSSE, "event: response.output_text.annotation.added") {
				t.Errorf("responses stream missing annotation.added:\n%s", respSSE)
			}

			chatAgg := renderChatAgg(t, mode.evs)
			chMsg := chatAgg["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
			chatAnns := chMsg["annotations"].([]any)
			if len(chatAnns) != 1 {
				t.Fatalf("chat agg annotations = %v, want 1", chatAnns)
			}
			uc := chatAnns[0].(map[string]any)["url_citation"].(map[string]any)
			if int(uc["start_index"].(float64)) != 7 || int(uc["end_index"].(float64)) != 13 {
				t.Errorf("chat agg url_citation = %+v, want [7, 13)", uc)
			}

			gemAgg, _ := renderGeminiAgg(t, mode.evs)
			cand := gemAgg["candidates"].([]any)[0].(map[string]any)
			gm, ok := cand["groundingMetadata"].(map[string]any)
			if !ok {
				t.Fatalf("gemini agg missing groundingMetadata: %+v", cand)
			}
			supports := gm["groundingSupports"].([]any)
			seg := supports[0].(map[string]any)["segment"].(map[string]any)
			if int(seg["partIndex"].(float64)) != 1 || int(seg["startIndex"].(float64)) != 0 || int(seg["endIndex"].(float64)) != 10 {
				t.Errorf("gemini agg segment = %+v, want partIndex=1 start=0 end=10", seg)
			}

			gemSSE, _ := renderGeminiStream(t, mode.evs)
			if !strings.Contains(gemSSE, `"groundingMetadata"`) || !strings.Contains(gemSSE, `"endIndex":17`) {
				t.Errorf("gemini stream missing groundingMetadata or wrong byte offsets:\n%s", gemSSE)
			}
		})
	}

	directMsgEvs, err := anthropicmessages.DecodeResponse(msgBody)
	if err != nil {
		t.Fatalf("direct messages decode: %v", err)
	}
	gotSame, _ := renderMessagesAgg(t, directMsgEvs)
	if !bytes.Equal(gotSame, msgBody) {
		t.Errorf("same-format messages changed bytes:\ngot:  %s\nwant: %s", gotSame, msgBody)
	}
}

func TestResponsesToChatGeminiMessages(t *testing.T) {
	respBody := []byte(`{
		"id": "resp_1",
		"object": "response",
		"model": "gpt-5",
		"status": "completed",
		"output": [
			{"id": "ws_1", "type": "web_search_call", "status": "completed", "action": {"type": "search", "query": "tokyo", "sources": [{"type": "url", "url": "https://example.com/tokyo"}]}},
			{"id": "msg_1", "type": "message", "status": "completed", "role": "assistant", "content": [
				{"type": "output_text", "text": "Hi 東京!", "annotations": [
					{"type": "url_citation", "url": "https://example.com/tokyo", "title": "Tokyo", "start_index": 3, "end_index": 5}
				]}
			]}
		],
		"usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}
	}`)

	nonStreamEvs, err := openairesponses.DecodeResponse(respBody)
	if err != nil {
		t.Fatalf("responses DecodeResponse: %v", err)
	}
	nonStreamEvs = roundTripEvents(t, nonStreamEvs)

	streamEvs := decodeResponsesStream(t, []string{
		`{"type":"response.created","sequence_number":1,"response":{"id":"resp_1","model":"gpt-5","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"tokyo","sources":[{"type":"url","url":"https://example.com/tokyo"}]}}}`,
		`{"type":"response.output_item.added","sequence_number":3,"output_index":1,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":4,"output_index":1,"content_index":0,"delta":"Hi 東京!"}`,
		`{"type":"response.output_text.annotation.added","sequence_number":5,"output_index":1,"content_index":0,"annotation_index":0,"annotation":{"type":"url_citation","url":"https://example.com/tokyo","title":"Tokyo","start_index":3,"end_index":5}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":1,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi 東京!","annotations":[{"type":"url_citation","url":"https://example.com/tokyo","title":"Tokyo","start_index":3,"end_index":5}]}]}}`,
		`{"type":"response.completed","sequence_number":7,"response":{"id":"resp_1","model":"gpt-5","status":"completed","output":[{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"tokyo","sources":[{"type":"url","url":"https://example.com/tokyo"}]}},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi 東京!","annotations":[{"type":"url_citation","url":"https://example.com/tokyo","title":"Tokyo","start_index":3,"end_index":5}]}]}]}}`,
	})

	for _, mode := range []struct {
		name string
		evs  []llm.Event
	}{
		{"nonstream", nonStreamEvs},
		{"stream", streamEvs},
	} {
		t.Run(mode.name, func(t *testing.T) {
			chatAgg := renderChatAgg(t, mode.evs)
			chMsg := chatAgg["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
			uc := chMsg["annotations"].([]any)[0].(map[string]any)["url_citation"].(map[string]any)
			if uc["url"] != "https://example.com/tokyo" || int(uc["start_index"].(float64)) != 3 || int(uc["end_index"].(float64)) != 5 {
				t.Errorf("chat agg url_citation = %+v", uc)
			}

			gemAgg, _ := renderGeminiAgg(t, mode.evs)
			gm := gemAgg["candidates"].([]any)[0].(map[string]any)["groundingMetadata"].(map[string]any)
			seg := gm["groundingSupports"].([]any)[0].(map[string]any)["segment"].(map[string]any)
			if int(seg["startIndex"].(float64)) != 3 || int(seg["endIndex"].(float64)) != 9 {
				t.Errorf("gemini agg segment = %+v, want [3, 9)", seg)
			}

			gemSSE, _ := renderGeminiStream(t, mode.evs)
			if !strings.Contains(gemSSE, `"groundingMetadata"`) || !strings.Contains(gemSSE, `"endIndex":9`) {
				t.Errorf("gemini stream missing groundingMetadata:\n%s", gemSSE)
			}

			msgBody, msgLosses := renderMessagesAgg(t, mode.evs)
			if strings.Contains(string(msgBody), "citations") {
				t.Errorf("messages agg emitted citations: %s", msgBody)
			}
			if len(msgLosses) == 0 {
				t.Errorf("messages agg reported no losses")
			}
		})
	}

	directRespEvs, err := openairesponses.DecodeResponse(respBody)
	if err != nil {
		t.Fatalf("direct responses decode: %v", err)
	}
	a := openairesponses.NewAggregator(llm.Request{Model: "gpt-5"})
	for _, ev := range directRespEvs {
		a.Add(ev)
	}
	gotSame, err := a.Response()
	if err != nil {
		t.Fatalf("same-format responses: %v", err)
	}
	if !bytes.Equal(gotSame, respBody) {
		t.Errorf("same-format responses changed bytes")
	}
}

func TestChatToResponsesGeminiMessages(t *testing.T) {
	chatBody := []byte(`{
		"id": "chatcmpl_1",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-5-search-api",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hi 🚀!",
				"annotations": [{
					"type": "url_citation",
					"url_citation": {"url": "https://example.com/rocket", "title": "Rocket", "start_index": 3, "end_index": 4}
				}]
			},
			"finish_reason": "stop"
		}]
	}`)

	nonStreamEvs, err := openaichat.DecodeResponse(chatBody)
	if err != nil {
		t.Fatalf("chat DecodeResponse: %v", err)
	}
	nonStreamEvs = roundTripEvents(t, nonStreamEvs)

	streamEvs := decodeChatStream(t, []string{
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-5-search-api","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi 🚀!"}}]}`,
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-5-search-api","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/rocket","title":"Rocket","start_index":3,"end_index":4}}]},"finish_reason":"stop"}]}`,
		`[DONE]`,
	})

	for _, mode := range []struct {
		name string
		evs  []llm.Event
	}{
		{"nonstream", nonStreamEvs},
		{"stream", streamEvs},
	} {
		t.Run(mode.name, func(t *testing.T) {
			respAgg, _ := renderResponsesAgg(t, mode.evs)
			ann := respAgg["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["annotations"].([]any)[0].(map[string]any)
			if ann["url"] != "https://example.com/rocket" || int(ann["start_index"].(float64)) != 3 || int(ann["end_index"].(float64)) != 4 {
				t.Errorf("responses agg ann = %+v", ann)
			}

			respSSE, _ := renderResponsesStream(t, mode.evs)
			if !strings.Contains(respSSE, "event: response.output_text.annotation.added") {
				t.Errorf("responses stream missing annotation.added:\n%s", respSSE)
			}

			gemAgg, _ := renderGeminiAgg(t, mode.evs)
			gm := gemAgg["candidates"].([]any)[0].(map[string]any)["groundingMetadata"].(map[string]any)
			seg := gm["groundingSupports"].([]any)[0].(map[string]any)["segment"].(map[string]any)
			if int(seg["startIndex"].(float64)) != 3 || int(seg["endIndex"].(float64)) != 7 {
				t.Errorf("gemini agg segment = %+v, want [3, 7)", seg)
			}

			msgBody, msgLosses := renderMessagesAgg(t, mode.evs)
			if strings.Contains(string(msgBody), "citations") {
				t.Errorf("messages agg emitted citations: %s", msgBody)
			}
			if len(msgLosses) == 0 {
				t.Errorf("messages agg reported no losses")
			}
		})
	}
}

func TestBadMetadataAndNonHTTPURLsNeverFailReply(t *testing.T) {
	badGemini := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Still works"}]},"finishReason":"STOP","groundingMetadata":"not-an-object"}]}`)
	evs, err := geminigenerate.DecodeResponse(badGemini)
	if err != nil {
		t.Fatalf("malformed groundingMetadata failed reply: %v", err)
	}
	chatAgg := renderChatAgg(t, evs)
	msg := chatAgg["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "Still works" || msg["annotations"] != nil {
		t.Errorf("malformed groundingMetadata chat msg = %+v", msg)
	}

	mixedGemini := []byte(`{
		"candidates": [{
			"index": 0,
			"content": {"role": "model", "parts": [{"text": "café"}]},
			"finishReason": "STOP",
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "javascript:alert(1)", "title": "XSS"}},
					{"web": {"uri": "https://example.com/ok", "title": "OK"}},
					{"web": {"uri": "data:text/plain,bad", "title": "Data"}}
				],
				"groundingSupports": [
					{"segment": {"startIndex": 0, "endIndex": 5}, "groundingChunkIndices": [0, 99, -1, 2]},
					{"segment": {"startIndex": 0, "endIndex": 4}, "groundingChunkIndices": [1]}
				]
			}
		}]
	}`)
	evs, err = geminigenerate.DecodeResponse(mixedGemini)
	if err != nil {
		t.Fatalf("mixed gemini failed: %v", err)
	}
	var itemCits []llm.Citation
	var finSources []llm.Source
	for _, ev := range evs {
		if ev.Type == llm.EventItemEnd {
			itemCits = append(itemCits, ev.Citations...)
		}
		if ev.Type == llm.EventFinish {
			finSources = ev.Sources
		}
	}
	if len(finSources) != 1 || finSources[0].URL != "https://example.com/ok" {
		t.Errorf("sources = %+v, want only https://example.com/ok", finSources)
	}
	if len(itemCits) != 1 || itemCits[0].URL != "https://example.com/ok" {
		t.Fatalf("citations = %+v, want only https://example.com/ok", itemCits)
	}
	if itemCits[0].StartIndex != nil || itemCits[0].EndIndex != nil {
		t.Errorf("mid-rune offset produced range [%v, %v], want nil range", itemCits[0].StartIndex, itemCits[0].EndIndex)
	}

	badMsg := []byte(`{
		"id":"msg_bad","type":"message","role":"assistant","model":"claude-opus-4-6",
		"content":[{"type":"text","text":"Hi","citations":[
			{"type":"web_search_result_location","url":"javascript:void(0)","title":"Bad","encrypted_index":"X"},
			{"type":"unknown_citation_kind","url":"https://example.com/u","title":"Unknown"}
		]}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	evs, err = anthropicmessages.DecodeResponse(badMsg)
	if err != nil {
		t.Fatalf("bad messages citations failed: %v", err)
	}
	for _, ev := range evs {
		if len(ev.Citations) != 0 {
			t.Errorf("expected bad/unknown citations to be dropped, got %+v", ev.Citations)
		}
	}
}

func TestNextTurnReplayAcrossAllTargets(t *testing.T) {
	targets := []struct {
		name   string
		encode func(llm.Request) ([]byte, []llm.Loss, error)
	}{
		{"claude", func(r llm.Request) ([]byte, []llm.Loss, error) {
			return anthropicmessages.EncodeRequest(r)
		}},
		{"responses", func(r llm.Request) ([]byte, []llm.Loss, error) {
			return openairesponses.EncodeRequest(r)
		}},
		{"chat", func(r llm.Request) ([]byte, []llm.Loss, error) {
			return openaichat.EncodeRequest(r)
		}},
		{"gemini", func(r llm.Request) ([]byte, []llm.Loss, error) {
			return geminigenerate.EncodeRequest(r)
		}},
	}

	replays := []struct {
		name  string
		parse func() (llm.Request, error)
	}{
		{
			name: "responses_with_url_citation",
			parse: func() (llm.Request, error) {
				return openairesponses.ParseRequest([]byte(`{
					"model": "claude-opus-4-6",
					"input": [
						{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hi"}]},
						{"type": "message", "role": "assistant", "content": [
							{"type": "output_text", "text": "Tokyo info", "annotations": [
								{"type": "url_citation", "url": "https://example.com/tokyo", "title": "Tokyo", "start_index": 0, "end_index": 5}
							]}
						]},
						{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "More?"}]}
					]
				}`))
			},
		},
		{
			name: "chat_with_annotations",
			parse: func() (llm.Request, error) {
				return openaichat.ParseRequest([]byte(`{
					"model": "claude-opus-4-6",
					"messages": [
						{"role": "user", "content": "Hi"},
						{"role": "assistant", "content": "Tokyo info", "annotations": [
							{"type": "url_citation", "url_citation": {"url": "https://example.com/tokyo", "title": "Tokyo", "start_index": 0, "end_index": 5}}
						]},
						{"role": "user", "content": "More?"}
					]
				}`))
			},
		},
		{
			name: "messages_cross_format_no_citations",
			parse: func() (llm.Request, error) {
				return anthropicmessages.ParseRequest([]byte(`{
					"model": "claude-opus-4-6",
					"max_tokens": 1024,
					"messages": [
						{"role": "user", "content": "Hi"},
						{"role": "assistant", "content": [{"type": "text", "text": "Tokyo info"}]},
						{"role": "user", "content": "More?"}
					]
				}`))
			},
		},
		{
			name: "gemini_model_turn",
			parse: func() (llm.Request, error) {
				return geminigenerate.ParseRequest("claude-opus-4-6", false, []byte(`{
					"contents": [
						{"role": "user", "parts": [{"text": "Hi"}]},
						{"role": "model", "parts": [{"text": "Tokyo info"}]},
						{"role": "user", "parts": [{"text": "More?"}]}
					]
				}`))
			},
		},
	}

	for _, rp := range replays {
		req, err := rp.parse()
		if err != nil {
			t.Fatalf("parse %s: %v", rp.name, err)
		}
		for _, tg := range targets {
			t.Run(rp.name+"_to_"+tg.name, func(t *testing.T) {
				body, _, err := tg.encode(req)
				if err != nil {
					t.Fatalf("encode %s -> %s: %v", rp.name, tg.name, err)
				}
				if !strings.Contains(string(body), "Tokyo info") {
					t.Errorf("encoded body lost assistant text: %s", body)
				}
				if tg.name == "claude" && (strings.Contains(string(body), "url_citation") || strings.Contains(string(body), "annotations")) {
					t.Errorf("claude encoder leaked foreign citation fields: %s", body)
				}
			})
		}
	}
}
