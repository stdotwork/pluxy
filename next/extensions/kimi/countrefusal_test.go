package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// Under the reject drop policy a count refuses exactly what generating the same request would:
// a Gemini request is generated as Chat Completions, which has no place for a model turn's
// thought, so its count is refused naming it; JSON output, which Chat Completions carries but
// the count's Messages body does not, is no reason to refuse.
func TestCountRefusalFollowsGenerating(t *testing.T) {
	gemini := func(body string, policy string) llm.Request {
		t.Helper()
		req, err := geminigenerate.ParseRequest("kimi-k2", false, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = policy
		return req
	}
	const thought = `{"contents":[{"role":"user","parts":[{"text":"Hi"}]},{"role":"model","parts":[{"text":"secret","thought":true},{"text":"Hello."}]},{"role":"user","parts":[{"text":"Again."}]}]}`
	const jsonOut = `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"generationConfig":{"responseMimeType":"application/json"}}`

	err := countRefusal(gemini(thought, llm.DropReject))
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || !slices.Equal(e.Dropped, []string{"contents[1].parts[0]"}) {
		t.Fatalf("thought under reject: err = %+v, want unsupported with dropped [contents[1].parts[0]]", err)
	}
	if _, _, gen := buildBody(gemini(thought, llm.DropReject), upChat, nil); gen == nil || gen.Error() != err.Error() {
		t.Errorf("generating refuses with %v, counting with %v; want the same refusal", gen, err)
	}
	for name, req := range map[string]llm.Request{
		"thought under report":    gemini(thought, llm.DropReport),
		"JSON output, reject":     gemini(jsonOut, llm.DropReject),
		"Messages client, reject": {Model: "kimi-k2", DropPolicy: llm.DropReject, Native: &llm.Native{Format: llm.FormatAnthropicMessages}},
	} {
		if err := countRefusal(req); err != nil {
			t.Errorf("%s: err = %v, want none", name, err)
		}
	}
}
