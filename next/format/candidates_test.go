package format_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// roundTripCandidateEvents marshals and unmarshals every event through JSON, checking the protocol crossing.
func roundTripCandidateEvents(t *testing.T, evs []llm.Event) []llm.Event {
	t.Helper()
	var seq llm.Sequence
	out := make([]llm.Event, 0, len(evs))
	for i, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event[%d]: %v", i, err)
		}
		var got llm.Event
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal event[%d]: %v", i, err)
		}
		if err := seq.Check(got); err != nil {
			t.Fatalf("event[%d] (%s) broke sequence: %v", i, got.Type, err)
		}
		out = append(out, got)
	}
	return out
}

func roundTripReq(t *testing.T, req llm.Request) llm.Request {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got llm.Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func parseSSEData(t *testing.T, raw string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			out = append(out, d)
		}
	}
	return out
}

func TestCandidateRequestsAcrossFormats(t *testing.T) {
	// Chat n=3 -> Gemini candidateCount=3 (on supported model + option)
	chatReq, err := openaichat.ParseRequest([]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}],"n":3}`))
	if err != nil {
		t.Fatal(err)
	}
	chatReq = roundTripReq(t, chatReq)
	if chatReq.Options.CandidateCount != 3 {
		t.Fatalf("chat CandidateCount = %d, want 3", chatReq.Options.CandidateCount)
	}
	gemBody, losses, err := geminigenerate.EncodeRequestWith(chatReq, geminigenerate.EncodeOptions{CandidateCount: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("chat->gemini losses = %+v, want none", losses)
	}
	var gemOut struct {
		GenerationConfig struct {
			CandidateCount *int `json:"candidateCount"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(gemBody, &gemOut); err != nil {
		t.Fatal(err)
	}
	if gemOut.GenerationConfig.CandidateCount == nil || *gemOut.GenerationConfig.CandidateCount != 3 {
		t.Fatalf("gemini candidateCount = %v, want 3 (body=%s)", gemOut.GenerationConfig.CandidateCount, gemBody)
	}

	// Gemini candidateCount=2 -> Chat n=2 (on supported model + option)
	gemReq, err := geminigenerate.ParseRequest("gpt-4o", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	gemReq = roundTripReq(t, gemReq)
	if gemReq.Options.CandidateCount != 2 {
		t.Fatalf("gemini CandidateCount = %d, want 2", gemReq.Options.CandidateCount)
	}
	chatBody, losses, err := openaichat.EncodeRequestWith(gemReq, openaichat.EncodeOptions{CandidateCount: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("gemini->chat losses = %+v, want none", losses)
	}
	var chatOut struct {
		N *int `json:"n"`
	}
	if err := json.Unmarshal(chatBody, &chatOut); err != nil {
		t.Fatal(err)
	}
	if chatOut.N == nil || *chatOut.N != 2 {
		t.Fatalf("chat n = %v, want 2 (body=%s)", chatOut.N, chatBody)
	}

	// Count 1 sends NO field and reports NO loss in either direction.
	chatOne, err := openaichat.ParseRequest([]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}],"n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b1, l1, err := geminigenerate.EncodeRequestWith(roundTripReq(t, chatOne), geminigenerate.EncodeOptions{CandidateCount: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(l1) != 0 || strings.Contains(string(b1), "candidateCount") {
		t.Errorf("chat n=1 -> gemini: losses=%+v body=%s", l1, b1)
	}

	gemOne, err := geminigenerate.ParseRequest("gpt-4o", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	b2, l2, err := openaichat.EncodeRequestWith(roundTripReq(t, gemOne), openaichat.EncodeOptions{CandidateCount: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(l2) != 0 || strings.Contains(string(b2), `"n"`) {
		t.Errorf("gemini candidateCount=1 -> chat: losses=%+v body=%s", l2, b2)
	}

	// Responses has no candidate count: reports a dropped loss at the source field.
	_, respLosses, err := openairesponses.EncodeRequest(chatReq)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(respLosses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "n" }) {
		t.Errorf("chat->responses losses = %+v, want dropped loss at n", respLosses)
	}
	_, respLosses2, err := openairesponses.EncodeRequest(gemReq)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(respLosses2, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "generationConfig.candidateCount" }) {
		t.Errorf("gemini->responses losses = %+v, want dropped loss at generationConfig.candidateCount", respLosses2)
	}
}

func TestGeminiToChatCandidatesNonStreaming(t *testing.T) {
	body := []byte(`{
		"responseId": "gem-1",
		"modelVersion": "gemini-2.5-pro",
		"candidates": [
			{
				"index": 0,
				"content": {"role": "model", "parts": [{"text": "First answer"}]},
				"finishReason": "STOP"
			},
			{
				"index": 1,
				"content": {"role": "model", "parts": [{"functionCall": {"id": "fc_1", "name": "lookup", "args": {"q": "go"}}}]},
				"finishReason": "STOP"
			},
			{
				"index": 2,
				"finishReason": "SAFETY"
			}
		],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 25, "totalTokenCount": 35}
	}`)
	evs, losses, err := geminigenerate.DecodeResponseLosses(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("decode losses = %+v, want none", losses)
	}
	evs = roundTripCandidateEvents(t, evs)

	agg := openaichat.NewAggregator()
	for _, ev := range evs {
		agg.Add(ev)
	}
	if agg.Size() == 0 {
		t.Error("aggregator Size() = 0")
	}
	out, err := agg.Response()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role      string  `json:"role"`
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Choices) != 3 {
		t.Fatalf("choices len = %d, want 3 (body=%s)", len(got.Choices), out)
	}
	if got.Choices[0].Index != 0 || got.Choices[0].FinishReason != "stop" || got.Choices[0].Message.Content == nil || *got.Choices[0].Message.Content != "First answer" {
		t.Errorf("choice[0] = %+v", got.Choices[0])
	}
	if got.Choices[1].Index != 1 || got.Choices[1].FinishReason != "tool_calls" || len(got.Choices[1].Message.ToolCalls) != 1 || got.Choices[1].Message.ToolCalls[0].Function.Name != "lookup" {
		t.Errorf("choice[1] = %+v", got.Choices[1])
	}
	if got.Choices[2].Index != 2 || got.Choices[2].FinishReason != "content_filter" {
		t.Errorf("choice[2] = %+v, want blocked content_filter", got.Choices[2])
	}
	// Usage counted once.
	if got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 25 {
		t.Errorf("usage = %+v, want prompt=10 completion=25", got.Usage)
	}

	// Candidate 0 isolation for Messages and Responses consumers.
	msgAgg := anthropicmessages.NewAggregator()
	respAgg := openairesponses.NewAggregator(llm.Request{Model: "gemini-2.5-pro"})
	for _, ev := range evs {
		msgAgg.Add(ev)
		respAgg.Add(ev)
	}
	msgOut, err := msgAgg.Response()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(msgOut), "lookup") {
		t.Errorf("anthropicmessages leaked candidate 1 tool call into candidate 0: %s", msgOut)
	}
	respOut, err := respAgg.Response()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(respOut), "lookup") {
		t.Errorf("openairesponses leaked candidate 1 tool call into candidate 0: %s", respOut)
	}
	if !slices.ContainsFunc(respAgg.Losses(), func(l llm.Loss) bool { return l.Path == "candidates" }) {
		t.Errorf("openairesponses losses = %+v, want candidates loss", respAgg.Losses())
	}
}

func TestGeminiToChatInterleavedStream(t *testing.T) {
	chunks := []string{
		`{"responseId":"g1","modelVersion":"gemini-2.5-pro","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"A1"}]}},{"index":1,"content":{"role":"model","parts":[{"text":"B1"}]}}]}`,
		`{"responseId":"g1","modelVersion":"gemini-2.5-pro","candidates":[{"index":1,"content":{"role":"model","parts":[{"text":"B2"}]},"finishReason":"MAX_TOKENS"}]}`,
		`{"responseId":"g1","modelVersion":"gemini-2.5-pro","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"A2"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":12,"totalTokenCount":17}}`,
	}
	dec := geminigenerate.NewStreamDecoder()
	var evs []llm.Event
	for _, ch := range chunks {
		e, err := dec.Decode([]byte(ch))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e...)
	}
	end, err := dec.Close()
	if err != nil {
		t.Fatal(err)
	}
	evs = roundTripCandidateEvents(t, append(evs, end...))

	var buf bytes.Buffer
	w := openaichat.NewStreamWriter(&buf, true)
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	lines := parseSSEData(t, buf.String())
	textByChoice := map[int]string{}
	finishByChoice := map[int]string{}
	usageChunks := 0
	for _, line := range lines {
		if line == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Index        int     `json:"index"`
				FinishReason *string `json:"finish_reason"`
				Delta        struct {
					Content *string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &ch); err != nil {
			t.Fatal(err)
		}
		if ch.Usage != nil {
			usageChunks++
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != nil {
				textByChoice[c.Index] += *c.Delta.Content
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				finishByChoice[c.Index] = *c.FinishReason
			}
		}
	}
	if textByChoice[0] != "A1A2" || textByChoice[1] != "B1B2" {
		t.Errorf("stream text = %+v, want 0:A1A2 1:B1B2", textByChoice)
	}
	if finishByChoice[0] != "stop" || finishByChoice[1] != "length" {
		t.Errorf("stream finish = %+v, want 0:stop 1:length", finishByChoice)
	}
	if usageChunks != 1 {
		t.Errorf("usage chunks = %d, want 1", usageChunks)
	}
}

func TestChatToGeminiCandidatesNonStreamingAndStream(t *testing.T) {
	// Non-streaming Chat with 3 choices (text, tool call, blocked) -> Gemini
	chatBody := []byte(`{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [
			{"index": 0, "message": {"role": "assistant", "content": "Choice zero"}, "finish_reason": "stop"},
			{"index": 1, "message": {"role": "assistant", "content": null, "tool_calls": [{"id": "call_b", "type": "function", "function": {"name": "calc", "arguments": "{\"x\":2}"}}]}, "finish_reason": "tool_calls"},
			{"index": 2, "message": {"role": "assistant", "content": ""}, "finish_reason": "content_filter"}
		],
		"usage": {"prompt_tokens": 8, "completion_tokens": 14, "total_tokens": 22}
	}`)
	evs, losses, err := openaichat.DecodeResponseLosses(chatBody)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("decode losses = %+v, want none", losses)
	}
	evs = roundTripCandidateEvents(t, evs)

	agg := geminigenerate.NewAggregator()
	for _, ev := range evs {
		agg.Add(ev)
	}
	out, err := agg.Response()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Candidates []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 3 {
		t.Fatalf("candidates len = %d, want 3 (body=%s)", len(got.Candidates), out)
	}
	if got.Candidates[0].Index != 0 || got.Candidates[0].FinishReason != "STOP" || got.Candidates[0].Content.Parts[0]["text"] != "Choice zero" {
		t.Errorf("cand[0] = %+v", got.Candidates[0])
	}
	if got.Candidates[1].Index != 1 || got.Candidates[1].FinishReason != "STOP" || got.Candidates[1].Content.Parts[0]["functionCall"] == nil {
		t.Errorf("cand[1] = %+v", got.Candidates[1])
	}
	if got.Candidates[2].Index != 2 || got.Candidates[2].FinishReason != "SAFETY" {
		t.Errorf("cand[2] = %+v, want SAFETY", got.Candidates[2])
	}
	if got.UsageMetadata.PromptTokenCount != 8 || got.UsageMetadata.CandidatesTokenCount != 14 {
		t.Errorf("usage = %+v", got.UsageMetadata)
	}

	// Streaming interleaved Chat -> Gemini
	streamChunks := []string{
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi "},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":1,"delta":{"role":"assistant","content":"Yo "},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"there"},"finish_reason":"stop"}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":1,"delta":{"content":"mate"},"finish_reason":"length"}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":9,"total_tokens":13}}`,
		`[DONE]`,
	}
	dec := openaichat.NewStreamDecoder()
	var sevs []llm.Event
	for _, ch := range streamChunks {
		e, err := dec.Decode([]byte(ch))
		if err != nil {
			t.Fatal(err)
		}
		sevs = append(sevs, e...)
	}
	sevs = roundTripCandidateEvents(t, sevs)

	var buf bytes.Buffer
	gw := geminigenerate.NewStreamWriter(&buf)
	for _, ev := range sevs {
		if err := gw.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	textByCand := map[int]string{}
	finishByCand := map[int]string{}
	usageCount := 0
	for _, line := range parseSSEData(t, buf.String()) {
		var chunk struct {
			Candidates []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finishReason"`
				Content      struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			UsageMetadata *struct {
				PromptTokenCount int64 `json:"promptTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.UsageMetadata != nil {
			usageCount++
		}
		for _, c := range chunk.Candidates {
			for _, p := range c.Content.Parts {
				textByCand[c.Index] += p.Text
			}
			if c.FinishReason != "" {
				finishByCand[c.Index] = c.FinishReason
			}
		}
	}
	if textByCand[0] != "Hi there" || textByCand[1] != "Yo mate" {
		t.Errorf("gemini stream text = %+v", textByCand)
	}
	if finishByCand[0] != "STOP" || finishByCand[1] != "MAX_TOKENS" {
		t.Errorf("gemini stream finish = %+v", finishByCand)
	}
	if usageCount != 1 {
		t.Errorf("usageMetadata emitted %d times, want 1", usageCount)
	}
}

func TestMalformedLaterCandidateDroppedWithLoss(t *testing.T) {
	// Gemini malformed candidate 1 does not fail reply.
	gemBody := []byte(`{
		"responseId": "g2",
		"modelVersion": "gemini-2.5-pro",
		"candidates": [
			{"index": 0, "content": {"role": "model", "parts": [{"text": "ok"}]}, "finishReason": "STOP"},
			"not-an-object"
		]
	}`)
	evs, losses, err := geminigenerate.DecodeResponseLosses(gemBody)
	if err != nil {
		t.Fatalf("malformed candidate 1 failed Gemini reply: %v", err)
	}
	if !slices.ContainsFunc(losses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "candidates[1]" }) {
		t.Errorf("Gemini losses = %+v, want dropped loss at candidates[1]", losses)
	}
	evs = roundTripCandidateEvents(t, evs)
	agg := openaichat.NewAggregator()
	for _, ev := range evs {
		agg.Add(ev)
	}
	out, err := agg.Response()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Errorf("Chat output after malformed Gemini cand 1: err=%v body=%s", err, out)
	}

	// Chat malformed choice 1 does not fail reply.
	chatBody := []byte(`{
		"id": "c2",
		"object": "chat.completion",
		"created": 1,
		"model": "gpt-4o",
		"choices": [
			{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			"bad-choice"
		]
	}`)
	cevs, closses, err := openaichat.DecodeResponseLosses(chatBody)
	if err != nil {
		t.Fatalf("malformed choice 1 failed Chat reply: %v", err)
	}
	if !slices.ContainsFunc(closses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "choices[1]" }) {
		t.Errorf("Chat losses = %+v, want dropped loss at choices[1]", closses)
	}
	_ = roundTripCandidateEvents(t, cevs)
}

func TestSameFormatCandidatesByteIdentical(t *testing.T) {
	// Gemini -> Gemini non-streaming byte-identical.
	gemRaw := []byte(`{"responseId":"g3","modelVersion":"gemini-2.5-pro","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"zero"}]},"finishReason":"STOP"},{"index":1,"content":{"role":"model","parts":[{"text":"one"}]},"finishReason":"STOP"}],"extraField":42}`)
	evs, err := geminigenerate.DecodeResponse(gemRaw)
	if err != nil {
		t.Fatal(err)
	}
	evs = roundTripCandidateEvents(t, evs)
	gAgg := geminigenerate.NewAggregator()
	for _, ev := range evs {
		gAgg.Add(ev)
	}
	gOut, err := gAgg.Response()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gOut, gemRaw) {
		t.Errorf("Gemini->Gemini non-streaming not byte-identical:\ngot:  %s\nwant: %s", gOut, gemRaw)
	}

	// Gemini -> Gemini streaming byte-identical per chunk.
	gDec := geminigenerate.NewStreamDecoder()
	sevs, err := gDec.Decode(gemRaw)
	if err != nil {
		t.Fatal(err)
	}
	send, err := gDec.Close()
	if err != nil {
		t.Fatal(err)
	}
	sevs = roundTripCandidateEvents(t, append(sevs, send...))
	var gBuf bytes.Buffer
	gw := geminigenerate.NewStreamWriter(&gBuf)
	for _, ev := range sevs {
		if err := gw.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	wantStream := "data: " + string(gemRaw) + "\n\n"
	if gBuf.String() != wantStream {
		t.Errorf("Gemini->Gemini streaming not byte-identical:\ngot:  %q\nwant: %q", gBuf.String(), wantStream)
	}

	// Chat -> Chat non-streaming preserves choice 1 verbatim (including extra fields).
	chatRaw := []byte(`{"id":"c3","object":"chat.completion","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"zero"},"finish_reason":"stop"},{"index":1,"message":{"role":"assistant","content":"one","custom_x":9},"finish_reason":"stop"}]}`)
	cevs, err := openaichat.DecodeResponse(chatRaw)
	if err != nil {
		t.Fatal(err)
	}
	cevs = roundTripCandidateEvents(t, cevs)
	cAgg := openaichat.NewAggregator()
	for _, ev := range cevs {
		cAgg.Add(ev)
	}
	cOut, err := cAgg.Response()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cOut, chatRaw) {
		t.Errorf("Chat->Chat non-streaming not byte-identical:\ngot:  %s\nwant: %s", cOut, chatRaw)
	}

	// Chat -> Chat streaming preserves choice 1 verbatim (including extra fields).
	chatStreamIn := []string{
		`{"id":"c4","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"zero"},"finish_reason":null}]}`,
		`{"id":"c4","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":1,"delta":{"role":"assistant","content":"one","custom_x":9},"finish_reason":"stop"}]}`,
		`{"id":"c4","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}
	cDec := openaichat.NewStreamDecoder()
	var csevs []llm.Event
	for _, line := range chatStreamIn {
		e, err := cDec.Decode([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		csevs = append(csevs, e...)
	}
	csevs = roundTripCandidateEvents(t, csevs)
	var cBuf bytes.Buffer
	cw := openaichat.NewStreamWriter(&cBuf, false)
	for _, ev := range csevs {
		if err := cw.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(cBuf.String(), `"custom_x":9`) {
		t.Errorf("Chat->Chat streaming lost choice 1 verbatim field: %s", cBuf.String())
	}
}

func TestLaterCandidatesCountInAggregatorSize(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	evs := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m", Format: llm.FormatGeminiGenerate}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "a"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateTextDelta, Delta: payload}},
		{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	cAgg := openaichat.NewAggregator()
	for _, ev := range evs {
		cAgg.Add(ev)
	}
	if cAgg.Size() < len(payload) {
		t.Errorf("openaichat Aggregator.Size() = %d, want >= %d", cAgg.Size(), len(payload))
	}

	evsChat := slices.Clone(evs)
	evsChat[0] = llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m", Format: llm.FormatOpenAIChat}}
	gAgg := geminigenerate.NewAggregator()
	for _, ev := range evsChat {
		gAgg.Add(ev)
	}
	if gAgg.Size() < len(payload) {
		t.Errorf("geminigenerate Aggregator.Size() = %d, want >= %d", gAgg.Size(), len(payload))
	}
}

func TestCandidateCountDroppedAcrossAllEncoders(t *testing.T) {
	chatReq, err := openaichat.ParseRequest([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"n":2}`))
	if err != nil {
		t.Fatal(err)
	}
	chatReq = roundTripReq(t, chatReq)

	gemReq, err := geminigenerate.ParseRequest("claude-sonnet-4-5", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	gemReq = roundTripReq(t, gemReq)

	hasDrop := func(losses []llm.Loss, path string) bool {
		return slices.ContainsFunc(losses, func(l llm.Loss) bool {
			return l.Kind == llm.LossDropped && l.Path == path
		})
	}

	t.Run("chat to anthropic.messages", func(t *testing.T) {
		_, losses, err := anthropicmessages.EncodeRequest(chatReq)
		if err != nil {
			t.Fatal(err)
		}
		if !hasDrop(losses, "n") {
			t.Errorf("anthropicmessages.EncodeRequest(chat n=2) losses = %+v, want dropped loss at n", losses)
		}
	})
	t.Run("gemini to anthropic.messages", func(t *testing.T) {
		_, losses, err := anthropicmessages.EncodeRequest(gemReq)
		if err != nil {
			t.Fatal(err)
		}
		if !hasDrop(losses, "generationConfig.candidateCount") {
			t.Errorf("anthropicmessages.EncodeRequest(gemini candidateCount=2) losses = %+v, want dropped loss at generationConfig.candidateCount", losses)
		}
	})
	t.Run("chat to openai.responses", func(t *testing.T) {
		_, losses, err := openairesponses.EncodeRequest(chatReq)
		if err != nil {
			t.Fatal(err)
		}
		if !hasDrop(losses, "n") {
			t.Errorf("openairesponses.EncodeRequest(chat n=2) losses = %+v, want dropped loss at n", losses)
		}
	})
	t.Run("gemini to openai.responses", func(t *testing.T) {
		_, losses, err := openairesponses.EncodeRequest(gemReq)
		if err != nil {
			t.Fatal(err)
		}
		if !hasDrop(losses, "generationConfig.candidateCount") {
			t.Errorf("openairesponses.EncodeRequest(gemini candidateCount=2) losses = %+v, want dropped loss at generationConfig.candidateCount", losses)
		}
	})
	t.Run("gemini to openai.chat default", func(t *testing.T) {
		body, losses, err := openaichat.EncodeRequest(gemReq)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `"n"`) || !hasDrop(losses, "generationConfig.candidateCount") {
			t.Errorf("openaichat.EncodeRequest(gemini candidateCount=2): losses=%+v body=%s", losses, body)
		}
	})
	t.Run("chat to gemini.generate_content default", func(t *testing.T) {
		body, losses, err := geminigenerate.EncodeRequest(chatReq)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "candidateCount") || !hasDrop(losses, "n") {
			t.Errorf("geminigenerate.EncodeRequest(chat n=2): losses=%+v body=%s", losses, body)
		}
	})
}

func TestSameFormatCandidateCountByteIdenticalToBase(t *testing.T) {
	const safetyOffJSON = `[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"OFF"},{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"OFF"},{"category":"HARM_CATEGORY_SEXUALLY_EXPLICIT","threshold":"OFF"},{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","threshold":"OFF"},{"category":"HARM_CATEGORY_CIVIC_INTEGRITY","threshold":"BLOCK_NONE"}]`

	t.Run("chat n=1", func(t *testing.T) {
		req, err := openaichat.ParseRequest([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"n":1}`))
		if err != nil {
			t.Fatal(err)
		}
		got, losses, err := openaichat.EncodeRequest(roundTripReq(t, req))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"messages":[{"role":"user","content":"hi"}],"model":"gpt-4o","n":1,"stream":false}`
		if string(got) != want || len(losses) != 0 {
			t.Errorf("chat n=1:\ngot:    %s (losses=%+v)\nwant:   %s", got, losses, want)
		}
	})

	t.Run("chat n=3", func(t *testing.T) {
		req, err := openaichat.ParseRequest([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"n":3}`))
		if err != nil {
			t.Fatal(err)
		}
		got, losses, err := openaichat.EncodeRequest(roundTripReq(t, req))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"messages":[{"role":"user","content":"hi"}],"model":"gpt-4o","n":3,"stream":false}`
		if string(got) != want || len(losses) != 0 {
			t.Errorf("chat n=3:\ngot:    %s (losses=%+v)\nwant:   %s", got, losses, want)
		}
	})

	t.Run("gemini candidateCount=1", func(t *testing.T) {
		req, err := geminigenerate.ParseRequest("gemini-2.5-pro", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":1}}`))
		if err != nil {
			t.Fatal(err)
		}
		got, losses, err := geminigenerate.EncodeRequest(roundTripReq(t, req))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"contents":[{"parts":[{"text":"hi"}],"role":"user"}],"generationConfig":{"candidateCount":1},"model":"gemini-2.5-pro","safetySettings":` + safetyOffJSON + `}`
		if string(got) != want || len(losses) != 0 {
			t.Errorf("gemini candidateCount=1:\ngot:    %s (losses=%+v)\nwant:   %s", got, losses, want)
		}
	})

	t.Run("gemini candidateCount=3", func(t *testing.T) {
		req, err := geminigenerate.ParseRequest("gemini-2.5-pro", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":3}}`))
		if err != nil {
			t.Fatal(err)
		}
		got, losses, err := geminigenerate.EncodeRequest(roundTripReq(t, req))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"contents":[{"parts":[{"text":"hi"}],"role":"user"}],"generationConfig":{"candidateCount":3},"model":"gemini-2.5-pro","safetySettings":` + safetyOffJSON + `}`
		if string(got) != want || len(losses) != 0 {
			t.Errorf("gemini candidateCount=3:\ngot:    %s (losses=%+v)\nwant:   %s", got, losses, want)
		}
	})
}

func TestGeminiCandidateCountUpperLimit(t *testing.T) {
	// At the documented maximum (8): carried with no loss.
	req8, err := openaichat.ParseRequest([]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}],"n":8}`))
	if err != nil {
		t.Fatal(err)
	}
	body8, losses8, err := geminigenerate.EncodeRequestWith(roundTripReq(t, req8), geminigenerate.EncodeOptions{CandidateCount: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body8), `"candidateCount":8`) || len(losses8) != 0 {
		t.Errorf("n=8 to gemini: losses=%+v body=%s", losses8, body8)
	}

	// Above the documented maximum (9): dropped with a reported loss.
	req9, err := openaichat.ParseRequest([]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}],"n":9}`))
	if err != nil {
		t.Fatal(err)
	}
	body9, losses9, err := geminigenerate.EncodeRequestWith(roundTripReq(t, req9), geminigenerate.EncodeOptions{CandidateCount: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body9), "candidateCount") {
		t.Errorf("n=9 to gemini sent candidateCount: %s", body9)
	}
	if !slices.ContainsFunc(losses9, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "n" }) {
		t.Errorf("n=9 to gemini losses = %+v, want dropped loss at n", losses9)
	}
}

func TestGeminiCandidateCountModelGating(t *testing.T) {
	supported := []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-2.0-flash",
		"gemini-2.0-flash-lite",
		"gemini-1.5-pro",
		"gemini-1.5-flash",
	}
	for _, model := range supported {
		t.Run("supported/"+model, func(t *testing.T) {
			req, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"n":2}`))
			if err != nil {
				t.Fatal(err)
			}
			body, losses, err := geminigenerate.EncodeRequestWith(roundTripReq(t, req), geminigenerate.EncodeOptions{CandidateCount: true})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"candidateCount":2`) || len(losses) != 0 {
				t.Errorf("%s: losses=%+v body=%s", model, losses, body)
			}
		})
	}

	unsupported := []string{
		"gemini-2.5-flash-image",
		"gemini-2.5-flash-preview-tts",
		"gemini-2.5-flash-native-audio-preview-12-2025",
		"gemini-2.5-computer-use-preview-10-2025",
		"gemini-3-flash-preview",
		"gemini-3.1-flash-image",
		"gemini-3.7-flash",
		"gemini-3.8-flash",
		"gemini-4-carbon",
		"gemini-4-neon",
		"gemini-flash-latest",
		"gemini-pro-latest",
		"gemini-1.0-pro",
	}
	for _, model := range unsupported {
		t.Run("unsupported/"+model, func(t *testing.T) {
			req, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"n":2}`))
			if err != nil {
				t.Fatal(err)
			}
			body, losses, err := geminigenerate.EncodeRequestWith(roundTripReq(t, req), geminigenerate.EncodeOptions{CandidateCount: true})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "candidateCount") {
				t.Errorf("%s: unexpectedly sent candidateCount: %s", model, body)
			}
			if !slices.ContainsFunc(losses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "n" }) {
				t.Errorf("%s: losses = %+v, want dropped loss at n", model, losses)
			}
		})
	}
}

func TestOpenAICandidateCountModelGating(t *testing.T) {
	supported := []string{
		"gpt-3.5-turbo",
		"gpt-4",
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-4o-2024-11-20",
		"gpt-4.1",
		"gpt-4.1-mini",
	}
	for _, model := range supported {
		t.Run("supported/"+model, func(t *testing.T) {
			req, err := geminigenerate.ParseRequest(model, false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`))
			if err != nil {
				t.Fatal(err)
			}
			body, losses, err := openaichat.EncodeRequestWith(roundTripReq(t, req), openaichat.EncodeOptions{CandidateCount: true})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"n":2`) || len(losses) != 0 {
				t.Errorf("%s: losses=%+v body=%s", model, losses, body)
			}
		})
	}

	unsupported := []string{
		"o1",
		"o3",
		"o3-mini",
		"o3-mini-2025-01-31",
		"o4-mini",
		"gpt-5",
	}
	for _, model := range unsupported {
		t.Run("unsupported/"+model, func(t *testing.T) {
			req, err := geminigenerate.ParseRequest(model, false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`))
			if err != nil {
				t.Fatal(err)
			}
			body, losses, err := openaichat.EncodeRequestWith(roundTripReq(t, req), openaichat.EncodeOptions{CandidateCount: true})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `"n"`) {
				t.Errorf("%s: unexpectedly sent n: %s", model, body)
			}
			if !slices.ContainsFunc(losses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "generationConfig.candidateCount" }) {
				t.Errorf("%s: losses = %+v, want dropped loss at generationConfig.candidateCount", model, losses)
			}
		})
	}
}
