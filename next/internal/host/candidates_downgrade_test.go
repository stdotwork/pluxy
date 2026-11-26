package host

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

// An extension below CandidatesMinor gets the same-format body it got before the minor.
func TestCandidateCountDowngradeKeepsSameFormatBody(t *testing.T) {
	chat := func(extra string) string {
		return `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]` + extra + `}`
	}
	gemini := func(cc string) string {
		return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":` + cc + `,"temperature":0.5}}`
	}
	type enc func(llm.Request) ([]byte, []llm.Loss, error)
	cases := []struct {
		name, want string
		parse      func() (llm.Request, error)
		encode     enc
		field      func(map[string]any) any
	}{
		{"chat n=1", "1", func() (llm.Request, error) { return openaichat.ParseRequest([]byte(chat(`,"n":1`))) }, openaichat.EncodeRequest,
			func(m map[string]any) any { return m["n"] }},
		{"chat n=3", "3", func() (llm.Request, error) { return openaichat.ParseRequest([]byte(chat(`,"n":3`))) }, openaichat.EncodeRequest,
			func(m map[string]any) any { return m["n"] }},
		{"gemini 1", "1", func() (llm.Request, error) {
			return geminigenerate.ParseRequest("gemini-2.5-flash", false, []byte(gemini("1")))
		}, geminigenerate.EncodeRequest,
			func(m map[string]any) any { return m["generationConfig"].(map[string]any)["candidateCount"] }},
		{"gemini 3", "3", func() (llm.Request, error) {
			return geminigenerate.ParseRequest("gemini-2.5-flash", false, []byte(gemini("3")))
		}, geminigenerate.EncodeRequest,
			func(m map[string]any) any { return m["generationConfig"].(map[string]any)["candidateCount"] }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := c.parse()
			if err != nil {
				t.Fatal(err)
			}
			down, lost := downgradeCandidateCount(CandidatesMinor-1, "old", req)
			if len(lost) != 0 || down.Options.CandidateCount != 0 {
				t.Fatalf("lost = %+v, count = %d; want no loss and no count", lost, down.Options.CandidateCount)
			}
			body, losses, err := c.encode(down)
			if err != nil || len(losses) != 0 {
				t.Fatalf("encode: losses %+v, err %v", losses, err)
			}
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			if got, _ := json.Marshal(c.field(m)); string(got) != c.want {
				t.Errorf("count in body = %s, want %s: %s", got, c.want, body)
			}
		})
	}
}

// An older extension's encoder for another format still reports the count as dropped.
func TestCandidateCountDowngradeStillReportedAcrossFormats(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"n":3}`))
	if err != nil {
		t.Fatal(err)
	}
	down, _ := downgradeCandidateCount(CandidatesMinor-1, "old", req)
	_, losses, err := geminigenerate.EncodeRequest(down)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range losses {
		if l.Path == "n" && l.Kind == llm.LossDropped {
			return
		}
	}
	t.Errorf("losses = %+v, want n dropped", losses)
}
