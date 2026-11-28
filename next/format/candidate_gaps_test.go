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

type chatChoices struct {
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
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
}

func chatReply(t *testing.T, evs []llm.Event) (chatChoices, string) {
	t.Helper()
	agg := openaichat.NewAggregator()
	for _, ev := range evs {
		agg.Add(ev)
	}
	out, err := agg.Response()
	if err != nil {
		t.Fatal(err)
	}
	var got chatChoices
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	return got, string(out)
}

func candHasLoss(losses []llm.Loss, path string) bool {
	return slices.ContainsFunc(losses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == path })
}

func candGeminiStream(t *testing.T, chunks ...string) ([]llm.Event, []llm.Loss) {
	t.Helper()
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
	return roundTripCandidateEvents(t, append(evs, end...)), dec.Losses()
}

func chatStream(t *testing.T, evs []llm.Event) string {
	t.Helper()
	var buf bytes.Buffer
	w := openaichat.NewStreamWriter(&buf, true)
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	return buf.String()
}

func TestGeminiMissingIndexIsCandidateZero(t *testing.T) {
	body := `{"candidates":[{"index":1,"content":{"parts":[{"text":"B"}]},"finishReason":"STOP"},{"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"}]}`
	evs, _, err := geminigenerate.DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	got, raw := chatReply(t, roundTripCandidateEvents(t, evs))
	if len(got.Choices) != 2 || got.Choices[0].Message.Content == nil || *got.Choices[0].Message.Content != "A" ||
		got.Choices[1].Message.Content == nil || *got.Choices[1].Message.Content != "B" {
		t.Errorf("non-streamed: %s", raw)
	}
	sevs, _ := candGeminiStream(t, body)
	got, raw = chatReply(t, sevs)
	if len(got.Choices) != 2 || *got.Choices[0].Message.Content != "A" || *got.Choices[1].Message.Content != "B" {
		t.Errorf("streamed: %s", raw)
	}
	evs, _, err = geminigenerate.DecodeResponseLosses([]byte(`{"candidates":[{"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"},{"content":{"parts":[{"text":"B"}]},"finishReason":"STOP"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, raw = chatReply(t, evs); len(got.Choices) != 2 || *got.Choices[0].Message.Content != "A" || *got.Choices[1].Message.Content != "B" {
		t.Errorf("both without index: %s", raw)
	}
}

func TestGeminiLaterCandidateCutOffIsReported(t *testing.T) {
	chunk := `{"candidates":[{"index":0,"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"cut off mid-sen"}]}}]}`
	evs, losses := candGeminiStream(t, chunk)
	if !candHasLoss(losses, "candidates[1].finishReason") {
		t.Errorf("losses = %+v, want one at candidates[1].finishReason", losses)
	}
	for _, line := range parseSSEData(t, chatStream(t, evs)) {
		var c chatChoicesStream
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		for _, ch := range c.Choices {
			if ch.Index == 1 && ch.FinishReason != nil && *ch.FinishReason != "length" {
				t.Errorf("candidate 1 finish_reason = %q, want length", *ch.FinishReason)
			}
		}
	}
	got, raw := chatReply(t, evs)
	if len(got.Choices) != 2 || got.Choices[0].FinishReason != "stop" || got.Choices[1].FinishReason != "length" {
		t.Errorf("aggregated: %s", raw)
	}
	devs, dlosses, err := geminigenerate.DecodeResponseLosses([]byte(chunk))
	if err != nil {
		t.Fatal(err)
	}
	if got, raw = chatReply(t, devs); got.Choices[1].FinishReason != "length" || !candHasLoss(dlosses, "candidates[1].finishReason") {
		t.Errorf("non-streamed: %s %+v", raw, dlosses)
	}
}

type chatChoicesStream struct {
	Choices []struct {
		Index        int     `json:"index"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func TestGeminiLaterCandidateLossesAreReported(t *testing.T) {
	body := `{"candidates":[
		{"index":0,"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"},
		{"index":1,"content":{"parts":[{"text":"B"},
			{"executableCode":{"language":"PYTHON","code":"print(1)"}},
			{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}},
			{"fileData":{"mimeType":"image/png","fileUri":"gs://b/x.png"}},
			{"functionCall":{"name":"f","args":{}},"thoughtSignature":"c2ln"}]},
		 "groundingMetadata":{"webSearchQueries":["q"]},"finishReason":"STOP"}]}`
	for name, decode := range map[string]func() ([]llm.Event, []llm.Loss){
		"whole": func() ([]llm.Event, []llm.Loss) {
			evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			return roundTripCandidateEvents(t, evs), losses
		},
		"stream": func() ([]llm.Event, []llm.Loss) { return candGeminiStream(t, body) },
	} {
		t.Run(name, func(t *testing.T) {
			evs, losses := decode()
			for _, path := range []string{
				"candidates[1].content.parts[1].executableCode",
				"candidates[1].content.parts[2].codeExecutionResult",
				"candidates[1].content.parts[3].fileData",
				"candidates[1].content.parts[4].thoughtSignature",
				"candidates[1].groundingMetadata",
			} {
				if !candHasLoss(losses, path) {
					t.Errorf("no loss at %s in %+v", path, losses)
				}
			}
			var finish llm.Event
			for _, ev := range evs {
				if ev.Type == llm.EventFinish {
					finish = ev
				}
			}
			if !slices.ContainsFunc(finish.CodeExecutions, func(c llm.CodeExecution) bool { return c.FilesOmitted }) {
				t.Errorf("runs = %+v, want the left-out record", finish.CodeExecutions)
			}
		})
	}
	evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
	if err != nil || len(evs) == 0 {
		t.Fatal(err)
	}
	if got := llm.ReplyLosses(losses, true); slices.ContainsFunc(got, func(l llm.Loss) bool { return strings.HasPrefix(l.Path, "candidates[1]") }) {
		t.Errorf("a client given the body is told it lost %+v", got)
	}
	if got := llm.ReplyLosses(losses, false); !candHasLoss(got, "candidates[1].groundingMetadata") {
		t.Errorf("a client built from the model is not told: %+v", got)
	}
}

func TestChatLaterChoiceAnnotationsAreReported(t *testing.T) {
	ann := `[{"type":"url_citation","url_citation":{"url":"https://example.com","title":"t","start_index":0,"end_index":1}}]`
	_, losses, err := openaichat.DecodeResponseLosses([]byte(`{"id":"c","choices":[
		{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},
		{"index":1,"message":{"role":"assistant","content":"b","annotations":` + ann + `},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !candHasLoss(losses, "choices[1].message.annotations") {
		t.Errorf("whole: losses = %+v", losses)
	}
	dec := openaichat.NewStreamDecoder()
	for _, ch := range []string{
		`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"a"}},{"index":1,"delta":{"role":"assistant","content":"b"}}]}`,
		`{"id":"c","choices":[{"index":1,"delta":{"annotations":` + ann + `}}]}`,
		`{"id":"c","choices":[{"index":1,"delta":{"annotations":` + ann + `}}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"},{"index":1,"delta":{},"finish_reason":"stop"}]}`,
	} {
		if _, err := dec.Decode([]byte(ch)); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, l := range dec.Losses() {
		if l.Path == "choices[1].delta.annotations" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("streamed: %d losses for choice 1's annotations, want 1: %+v", n, dec.Losses())
	}
}

func TestGeminiCandidateCountSameFormatAsWritten(t *testing.T) {
	for _, written := range []string{`"x"`, `1.5`, `2147483648`, `"2"`, `2.0`, `0`, `-1`, `true`} {
		body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":` + written + `}}`
		req, err := geminigenerate.ParseRequest("gemini-2.5-pro", false, []byte(body))
		if err != nil {
			t.Errorf("candidateCount %s refused: %v", written, err)
			continue
		}
		if req.Options.CandidateCount != 0 {
			t.Errorf("candidateCount %s read as %d", written, req.Options.CandidateCount)
		}
		out, _, err := geminigenerate.EncodeRequest(roundTripReq(t, req))
		if err != nil {
			t.Errorf("candidateCount %s: encode: %v", written, err)
			continue
		}
		var got struct {
			GenerationConfig struct {
				CandidateCount json.RawMessage `json:"candidateCount"`
			} `json:"generationConfig"`
		}
		if json.Unmarshal(out, &got) != nil || string(got.GenerationConfig.CandidateCount) != written {
			t.Errorf("candidateCount %s went on as %s", written, got.GenerationConfig.CandidateCount)
		}
	}
	req, err := geminigenerate.ParseRequest("gemini-2.5-pro", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`))
	if err != nil || req.Options.CandidateCount != 2 {
		t.Errorf("a whole number is a count: %v, %d", err, req.Options.CandidateCount)
	}
}

func TestSameFormatChatKeepsNegativeIndexChoice(t *testing.T) {
	evs, _, err := openaichat.DecodeResponseLosses([]byte(`{"id":"c","object":"chat.completion","model":"m","choices":[
		{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},
		{"index":-1,"message":{"role":"assistant","content":"neg"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	agg := openaichat.NewAggregator()
	for _, ev := range evs {
		agg.Add(ev)
	}
	out, err := agg.Response()
	if err != nil || !strings.Contains(string(out), `"index":-1`) || !strings.Contains(string(out), `"neg"`) {
		t.Errorf("non-streamed: %v %s", err, out)
	}
	dec := openaichat.NewStreamDecoder()
	var sevs []llm.Event
	for _, ch := range []string{
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"a"}},{"index":-1,"delta":{"content":"neg"}}]}`,
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	} {
		e, err := dec.Decode([]byte(ch))
		if err != nil {
			t.Fatal(err)
		}
		sevs = append(sevs, e...)
	}
	if s := chatStream(t, sevs); !strings.Contains(s, `"index":-1`) || !strings.Contains(s, `"neg"`) {
		t.Errorf("streamed: %s", s)
	}
}

func TestGeminiMalformedCallInLaterCandidateIsMapped(t *testing.T) {
	body := `{"candidates":[{"index":0,"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"},{"index":1,"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"bad"}]}`
	evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !candHasLoss(losses, "candidates[1].finishReason") {
		t.Errorf("losses = %+v", losses)
	}
	got, raw := chatReply(t, evs)
	if strings.Contains(raw, "MALFORMED") || got.Choices[1].FinishReason != "stop" {
		t.Errorf("non-streamed: %s", raw)
	}
	sevs, slosses := candGeminiStream(t, body)
	if s := chatStream(t, sevs); strings.Contains(s, "MALFORMED") || !candHasLoss(slosses, "candidates[1].finishReason") {
		t.Errorf("streamed: %s %+v", s, slosses)
	}
}

func TestChatRepeatedFinishOnLaterChoiceForwardedOnce(t *testing.T) {
	dec := openaichat.NewStreamDecoder()
	var evs []llm.Event
	for _, ch := range []string{
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"a"}},{"index":1,"delta":{"role":"assistant","content":"b"}}]}`,
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"},{"index":1,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c","model":"m","choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	} {
		e, err := dec.Decode([]byte(ch))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e...)
	}
	evs = roundTripCandidateEvents(t, evs)
	n := 0
	for _, ev := range evs {
		if ev.Candidate != nil && ev.Candidate.Index == 1 && ev.Candidate.Kind == llm.CandidateFinish {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d finish events for candidate 1, want 1", n)
	}
	var buf bytes.Buffer
	w := geminigenerate.NewStreamWriter(&buf)
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	finishes := 0
	for _, line := range parseSSEData(t, buf.String()) {
		var c struct {
			Candidates []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		for _, cand := range c.Candidates {
			if cand.Index == 1 && cand.FinishReason != "" {
				finishes++
			}
		}
	}
	if finishes != 1 {
		t.Errorf("the Gemini client got %d finishes for candidate 1, want 1: %s", finishes, buf.String())
	}
}

func TestReplayChosenLaterCandidateNextTurn(t *testing.T) {
	evs, _, err := geminigenerate.DecodeResponseLosses([]byte(`{"candidates":[
		{"index":0,"content":{"parts":[{"text":"ZERO-TEXT"}]},"finishReason":"STOP"},
		{"index":1,"content":{"parts":[{"functionCall":{"id":"fc_1","name":"lookup","args":{"q":"go"}}}]},"finishReason":"STOP"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	reply, raw := chatReply(t, roundTripCandidateEvents(t, evs))
	if len(reply.Choices) != 2 || len(reply.Choices[1].Message.ToolCalls) != 1 {
		t.Fatalf("reply: %s", raw)
	}
	var whole struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal([]byte(raw), &whole); err != nil {
		t.Fatal(err)
	}
	var chosen struct {
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(whole.Choices[1], &chosen); err != nil {
		t.Fatal(err)
	}
	next := `{"model":"gemini-2.5-pro","messages":[
		{"role":"user","content":"find go"},` + string(chosen.Message) + `,
		{"role":"tool","tool_call_id":"fc_1","content":"Go is a language"}]}`
	req, err := openaichat.ParseRequest([]byte(next))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	req = roundTripReq(t, req)
	for name, encode := range map[string]func(llm.Request) ([]byte, []llm.Loss, error){
		"gemini":    geminigenerate.EncodeRequest,
		"chat":      openaichat.EncodeRequest,
		"responses": openairesponses.EncodeRequest,
		"messages":  anthropicmessages.EncodeRequest,
	} {
		body, _, err := encode(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{"lookup", "fc_1", "Go is a language"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s: the replayed turn lacks %q: %s", name, want, body)
			}
		}
		if plain := strings.ReplaceAll(string(body), `\"`, `"`); !strings.Contains(plain, `"q":"go"`) {
			t.Errorf("%s: the replayed call lost its arguments: %s", name, body)
		}
		if strings.Contains(string(body), "ZERO-TEXT") {
			t.Errorf("%s: candidate 0 came back with candidate 1's turn: %s", name, body)
		}
	}
}
