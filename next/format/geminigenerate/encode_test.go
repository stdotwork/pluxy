package geminigenerate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

const geminiSig = "CiQB0e2Kb7sigA1"

func TestEncodeChatRequest(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"gemini-x","temperature":0.3,"max_tokens":9,"stop":["END"],
		"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],
		"response_format":{"type":"json_schema","json_schema":{"name":"w","schema":{"type":"object"}}},
		"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"12C"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	_ = json.Unmarshal(body, &got)
	sameJSON(t, "systemInstruction", got["systemInstruction"], `{"parts":[{"text":"Be brief."}]}`)
	sameJSON(t, "contents", got["contents"], `[
		{"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},
		{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"get_weather","args":{"city":"Oslo"}},"thoughtSignature":"skip_thought_signature_validator"}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"get_weather","response":{"result":"12C"}}}]}]`)
	sameJSON(t, "generationConfig", got["generationConfig"], `{"temperature":0.3,"maxOutputTokens":9,"stopSequences":["END"],"responseMimeType":"application/json","responseJsonSchema":{"type":"object"}}`)
	sameJSON(t, "tools", got["tools"], `[{"functionDeclarations":[{"name":"get_weather","parametersJsonSchema":{"type":"object"}}]}]`)
	if got["safetySettings"] == nil {
		t.Error("no safetySettings")
	}
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

func TestEncodeParallelToolCallsFalse(t *testing.T) {
	req, _ := openaichat.ParseRequest([]byte(`{"model":"m","parallel_tool_calls":false,"tool_choice":"required",
		"tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"hi"}]}`))
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}`) {
		t.Errorf("body %s: want function calling mode NONE", body)
	}
	if len(losses) != 1 || losses[0].Path != "parallel_tool_calls" || losses[0].Kind != llm.LossApproximated {
		t.Errorf("losses = %+v, want parallel_tool_calls approximated", losses)
	}
}

// TestThoughtSignatureCarriesOver follows a Gemini thought signature through a Messages and a
// Responses client: the decoder gives it as a reasoning item's opaque data, and when the client
// replays it, the encoder puts it back on the part after it.
func TestThoughtSignatureCarriesOver(t *testing.T) {
	d := NewStreamDecoder()
	evs, err := d.Decode([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{}},"thoughtSignature":"` + geminiSig + `"}]},"index":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var opaque *llm.Opaque
	for _, ev := range evs {
		if ev.Type == llm.EventItemEnd && ev.Opaque != nil {
			opaque = ev.Opaque
			break
		}
		if ev.Type == llm.EventItemStart && ev.Item.Kind == llm.ItemToolCall {
			t.Fatal("the tool call opened before the reasoning item holding its signature")
		}
	}
	if opaque == nil || opaque.Format != llm.FormatGeminiGenerate || opaque.Data != geminiSig {
		t.Fatalf("events %+v: want a reasoning item carrying the signature", evs)
	}

	msgs, err := anthropicmessages.ParseRequest([]byte(`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + geminiSig + `"},{"type":"tool_use","id":"t1","name":"f","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := openairesponses.ParseRequest([]byte(`{"model":"m","input":[{"role":"user","content":"hi"},
		{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"` + geminiSig + `"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Calling."}]},
		{"type":"function_call","call_id":"t1","name":"f","arguments":"{}"},
		{"type":"function_call_output","call_id":"t1","output":"ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		req  llm.Request
		want string
	}{
		"messages":  {msgs, `{"role":"model","parts":[{"functionCall":{"id":"t1","name":"f","args":{}},"thoughtSignature":"` + geminiSig + `"}]}`},
		"responses": {resp, `{"role":"model","parts":[{"text":"Calling.","thoughtSignature":"` + geminiSig + `"},{"functionCall":{"id":"t1","name":"f","args":{}},"thoughtSignature":"skip_thought_signature_validator"}]}`},
	} {
		body, losses, err := EncodeRequest(c.req)
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Contents []json.RawMessage }
		_ = json.Unmarshal(body, &got)
		if len(got.Contents) != 3 {
			t.Fatalf("%s: contents %s", name, body)
		}
		sameJSON(t, name, got.Contents[1], c.want)
		for _, l := range losses {
			if strings.Contains(l.Detail, "reasoning") {
				t.Errorf("%s: the signature was reported lost: %+v", name, l)
			}
		}
	}

	// A Claude signature is not Gemini's: it is dropped and reported.
	claude, _ := anthropicmessages.ParseRequest([]byte(`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"EqQBCkgIARABGAIiQ"},{"type":"text","text":"ok"}]}]}`))
	body, losses, _ := EncodeRequest(claude)
	if strings.Contains(string(body), "EqQB") || len(losses) == 0 {
		t.Errorf("a Claude signature reached Gemini (%s) or was not reported (%+v)", body, losses)
	}
}

func TestIsThoughtSignature(t *testing.T) {
	for s, want := range map[string]bool{
		geminiSig:                  true,
		"EqQBCkgIARABGAIiQ":        false, // Claude
		"gAAAAABpZ3VhcmQ":          false, // OpenAI
		"":                         false,
		"Cg==":                     true,
		"C!notbase64":              false,
		strings.Repeat("C", 1<<21): false,
	} {
		if got := IsThoughtSignature(s); got != want {
			t.Errorf("IsThoughtSignature(%.20q) = %v, want %v", s, got, want)
		}
	}
}
