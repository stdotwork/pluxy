package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func fp(v float64) *float64 { return &v }

func samplingRequest(model string, temperature, topP *float64, effort string) llm.Request {
	maxTokens := 32000
	return llm.Request{
		Model:   model,
		Items:   []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}}}},
		Options: llm.Options{Temperature: temperature, TopP: topP, ReasoningEffort: effort, MaxOutputTokens: &maxTokens},
		Native:  &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)},
	}
}

func TestBuildBodyDropsRefusedSampling(t *testing.T) {
	for _, c := range []struct {
		name                 string
		model                string
		temperature, topP    *float64
		effort               string
		wantSent, wantLosses []string
	}{
		{"4.7 both", "claude-opus-4-7", fp(0.5), fp(0.9), "", nil, []string{"temperature", "top_p"}},
		{"5.5 defaults", "claude-sonnet-5-5", fp(1), fp(0.99), "", nil, []string{"temperature", "top_p"}},
		{"5.5 default top_p alone", "claude-haiku-5-5-20261001", nil, fp(0.99), "high", []string{"top_p"}, nil},
		{"5.5 default temperature alone", "claude-sonnet-5-5", fp(1), nil, "", []string{"temperature"}, nil},
		{"5.5 top_p alone", "claude-haiku-5-5", nil, fp(0.9), "", nil, []string{"top_p"}},
		{"Mythos 5", "claude-mythos-5", fp(0.2), nil, "", nil, []string{"temperature"}},
		{"unplaced claude name", "claude-test", fp(0.5), fp(0.9), "", nil, []string{"temperature", "top_p"}},
		{"unplaced claude name, default", "claude-mythos-preview", fp(1), nil, "", nil, []string{"temperature"}},
		{"context window suffix", "claude-opus-4-7[1m]", fp(0.5), nil, "", nil, []string{"temperature"}},
		{"Vertex AI version", "claude-opus-4-7@20260416", fp(0.5), nil, "", nil, []string{"temperature"}},
		{"Bedrock profile", "us.anthropic.claude-opus-4-7", fp(0.5), nil, "", nil, []string{"temperature"}},
		{"upper case", "Claude-Opus-4-7", fp(0.5), nil, "", nil, []string{"temperature"}},
		{"Vertex AI 4.0, both", "claude-sonnet-4@20250514", fp(0.5), fp(0.9), "", []string{"temperature"}, []string{"top_p"}},
		{"4.5 budget thinking", "claude-sonnet-4-5", fp(0.5), fp(0.9), "high", nil, []string{"temperature", "top_p"}},
		{"4.6 adaptive thinking", "claude-opus-4-6", fp(0.7), nil, "medium", nil, []string{"temperature"}},
		{"3.7 thinking, temperature 1", "claude-3-7-sonnet-20250219", fp(1), nil, "low", []string{"temperature"}, nil},
		{"4.5 thinking, top_p 0.95", "claude-sonnet-4-5", nil, fp(0.95), "high", []string{"top_p"}, nil},
		{"4.6 thinking, both allowed", "claude-opus-4-6", fp(1), fp(0.97), "medium", []string{"temperature"}, []string{"top_p"}},
		{"4.5 both", "claude-sonnet-4-5", fp(0.5), fp(0.9), "", []string{"temperature"}, []string{"top_p"}},
		{"4.6 thinking off, both", "claude-sonnet-4-6", fp(0.5), fp(0.9), "none", []string{"temperature"}, []string{"top_p"}},
		{"4.6 temperature alone", "claude-sonnet-4-6", fp(0.5), nil, "", []string{"temperature"}, nil},
		{"4.1 top_p alone", "claude-opus-4-1-20250805", nil, fp(0.9), "", []string{"top_p"}, nil},
		{"other model", "glm-5", fp(0.5), fp(0.9), "", []string{"temperature", "top_p"}, nil},
		{"claude inside another name", "my-claude-model", fp(0.5), fp(0.9), "", []string{"temperature", "top_p"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, tr, err := buildBody(samplingRequest(c.model, c.temperature, c.topP, c.effort))
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			for key, v := range map[string]*float64{"temperature": c.temperature, "top_p": c.topP} {
				got, sent := m[key]
				switch want := slices.Contains(c.wantSent, key); {
				case want && !sent:
					t.Errorf("%s not sent; body %s", key, body)
				case want && string(got) != string(must(json.Marshal(*v))):
					t.Errorf("%s = %s, want %v", key, got, *v)
				case !want && sent:
					t.Errorf("%s sent as %s, want it left out", key, got)
				}
			}
			var dropped []string
			for _, l := range tr.Losses {
				if (l.Path == "temperature" || l.Path == "top_p") && l.Kind == llm.LossDropped && l.Detail != "" {
					dropped = append(dropped, l.Path)
				}
			}
			if !slices.Equal(dropped, c.wantLosses) {
				t.Errorf("reported dropped %v, want %v (losses %+v)", dropped, c.wantLosses, tr.Losses)
			}
		})
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestBuildBodyRejectsRefusedSampling(t *testing.T) {
	req := samplingRequest("claude-opus-4-7", fp(0.5), nil, "")
	req.DropPolicy = llm.DropReject
	_, _, err := buildBody(req)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || !strings.Contains(e.Message, "temperature") {
		t.Fatalf("err = %v, want unsupported naming temperature", err)
	}
	req = samplingRequest("claude-sonnet-4-6", fp(0.5), nil, "")
	req.DropPolicy = llm.DropReject
	if _, _, err := buildBody(req); err != nil {
		t.Fatalf("temperature alone on Claude Sonnet 4.6: %v, want it sent", err)
	}
}

func TestBuildBodyKeepsMessagesSampling(t *testing.T) {
	in := `{"model":"claude-opus-4-7","max_tokens":10,"temperature":0.5,"top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`
	body, tr, err := buildBody(llm.Request{Model: "claude-opus-4-7", Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(in)}})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != in || !tr.Passthrough || len(tr.Losses) != 0 {
		t.Errorf("passthrough %v, losses %+v, body %s; want the client's body unchanged", tr.Passthrough, tr.Losses, body)
	}
}
