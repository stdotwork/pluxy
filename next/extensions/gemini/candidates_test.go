package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestCandidateCountHostAndModelGating(t *testing.T) {
	chatReq := func(model string, stream bool, n int) llm.Request {
		r, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Stream = stream
		r.Options.CandidateCount = n
		return r
	}

	cases := []struct {
		name     string
		req      llm.Request
		settings settings
		wantN    int
		wantLoss bool
	}{
		{"gemini api 2.5 pro non-stream", chatReq("gemini-2.5-pro", false, 3), apiSettings, 3, false},
		{"gemini api 2.5 pro stream", chatReq("gemini-2.5-pro", true, 3), apiSettings, 3, false},
		{"gemini api 1.0 pro unsupported", chatReq("gemini-1.0-pro", false, 3), apiSettings, 0, true},
		{"vertex non-stream supported", chatReq("gemini-2.5-pro", false, 2), vertexSettingsTest, 2, false},
		{"vertex stream unsupported", chatReq("gemini-2.5-pro", true, 2), vertexSettingsTest, 0, true},
		{"custom host unsupported", chatReq("gemini-2.5-pro", false, 2), customHost, 0, true},
		{"count 1 sends nothing", chatReq("gemini-2.5-pro", false, 1), apiSettings, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, tr, err := buildBodyOn(tc.req, tc.settings)
			if err != nil {
				t.Fatalf("buildBodyOn: %v", err)
			}
			var got struct {
				GenerationConfig struct {
					CandidateCount *int `json:"candidateCount"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if tc.wantN == 0 {
				if got.GenerationConfig.CandidateCount != nil || strings.Contains(string(body), "candidateCount") {
					t.Errorf("unexpected candidateCount in body: %s", body)
				}
			} else if got.GenerationConfig.CandidateCount == nil || *got.GenerationConfig.CandidateCount != tc.wantN {
				t.Errorf("candidateCount = %v, want %d (body=%s)", got.GenerationConfig.CandidateCount, tc.wantN, body)
			}
			hasLoss := slices.ContainsFunc(tr.Losses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "n" })
			if hasLoss != tc.wantLoss {
				t.Errorf("hasLoss = %v, want %v (losses=%+v)", hasLoss, tc.wantLoss, tr.Losses)
			}
		})
	}
}
