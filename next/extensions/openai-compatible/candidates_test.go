package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestCandidateCountHostAndModelGating(t *testing.T) {
	gemReq := func(model string, n int) llm.Request {
		r, err := geminigenerate.ParseRequest(model, false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Options.CandidateCount = n
		return r
	}
	chatReq := func(model string, n int) llm.Request {
		r, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Options.CandidateCount = n
		return r
	}

	for _, tc := range []struct {
		name     string
		base     string
		model    string
		n        int
		wantN    int
		wantLoss bool
	}{
		{"openai gpt-4o", "https://api.openai.com/v1", "gpt-4o", 3, 3, false},
		{"openai gpt-5 newer model", "https://api.openai.com/v1", "gpt-5", 3, 0, true},
		{"third-party host", "https://llm.example.com/v1", "gpt-4o", 3, 0, true},
		{"count 1", "https://api.openai.com/v1", "gpt-4o", 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, tr, err := upstreamBody(gemReq(tc.model, tc.n), llm.FormatOpenAIChat, settings{BaseURL: tc.base})
			if err != nil {
				t.Fatalf("upstreamBody: %v", err)
			}
			var got struct {
				N *int `json:"n"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if tc.wantN == 0 {
				if got.N != nil || strings.Contains(string(body), `"n"`) {
					t.Errorf("unexpected n in body: %s", body)
				}
			} else if got.N == nil || *got.N != tc.wantN {
				t.Errorf("n = %v, want %d (body=%s)", got.N, tc.wantN, body)
			}
			hasLoss := slices.ContainsFunc(tr.Losses, func(l llm.Loss) bool {
				return l.Kind == llm.LossDropped && l.Path == "generationConfig.candidateCount"
			})
			if hasLoss != tc.wantLoss {
				t.Errorf("hasLoss = %v, want %v (losses=%+v)", hasLoss, tc.wantLoss, tr.Losses)
			}
		})
	}

	body, tr, err := upstreamBody(chatReq("gemini-2.5-pro", 2), llm.FormatGeminiGenerate, settings{BaseURL: "https://generativelanguage.googleapis.com/v1beta"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"candidateCount":2`) || len(tr.Losses) != 0 {
		t.Errorf("gemini api via openai-compatible: losses=%+v body=%s", tr.Losses, body)
	}
	body3p, tr3p, err := upstreamBody(chatReq("gemini-2.5-pro", 2), llm.FormatGeminiGenerate, settings{BaseURL: "https://llm.example.com/v1beta"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body3p), "candidateCount") || !tr3p.Dropped() {
		t.Errorf("third-party gemini via openai-compatible: losses=%+v body=%s", tr3p.Losses, body3p)
	}
}
