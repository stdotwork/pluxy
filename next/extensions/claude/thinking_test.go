package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// fields returns the named top-level fields of a JSON body, as JSON, with "-" for one that is
// absent.
func fields(body []byte, names ...string) string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(body, &m)
	var out []string
	for _, n := range names {
		v := string(m[n])
		if v == "" {
			v = "-"
		}
		var x any
		if json.Unmarshal(m[n], &x) == nil {
			b, _ := json.Marshal(x)
			v = string(b)
		}
		out = append(out, n+"="+v)
	}
	return strings.Join(out, " ")
}

func lossPaths(tr *llm.Translation) []string {
	var out []string
	for _, l := range tr.Losses {
		out = append(out, l.Kind+" "+l.Path)
	}
	slices.Sort(out)
	return out
}

// A translated request's reasoning effort and tool choice reach each model family in the form
// that model takes (Anthropic's effort and thinking tables), each difference reported.
func TestBuildBodyThinkingPerModel(t *testing.T) {
	tool := llm.Tool{Name: "w", Parameters: json.RawMessage(`{"type":"object"}`)}
	for _, c := range []struct {
		name, model, effort string
		choice              *llm.ToolChoice
		want                string
		losses              []string
	}{
		{"fable xhigh", "claude-fable-5-2", "xhigh", nil,
			`thinking={"type":"adaptive"} output_config={"effort":"xhigh"} tool_choice=-`, nil},
		{"mythos none, forced tool", "claude-mythos-5-1", "none", &llm.ToolChoice{Mode: llm.ToolChoiceRequired},
			`thinking=- output_config={"effort":"low"} tool_choice={"type":"auto"}`, []string{"approximated thinking", "approximated tool_choice"}},
		{"opus 5.5 none", "claude-opus-5-5", "none", nil,
			`thinking=- output_config={"effort":"low"} tool_choice=-`, []string{"approximated thinking"}},
		{"opus 5.5 named tool", "claude-opus-5-5", "high", &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: "w"},
			`thinking={"type":"adaptive"} output_config={"effort":"high"} tool_choice={"type":"auto"}`, []string{"approximated tool_choice"}},
		{"opus 5 none", "claude-opus-5", "none", nil,
			`thinking={"type":"disabled"} output_config=- tool_choice=-`, nil},
		{"opus 5 xhigh, forced tool", "claude-opus-5", "xhigh", &llm.ToolChoice{Mode: llm.ToolChoiceRequired},
			`thinking={"type":"adaptive"} output_config={"effort":"xhigh"} tool_choice={"type":"any"}`, nil},
		{"sonnet 5.5 none", "claude-sonnet-5-5", "none", nil,
			`thinking={"type":"between_tools"} output_config=- tool_choice=-`, []string{"approximated thinking.type"}},
		{"sonnet 5.5 xhigh", "claude-sonnet-5-5", "xhigh", nil,
			`thinking={"type":"adaptive"} output_config={"effort":"xhigh"} tool_choice=-`, nil},
		{"haiku 5.5 none", "claude-haiku-5-5", "none", nil,
			`thinking={"type":"disabled"} output_config=- tool_choice=-`, nil},
		{"haiku 5.5 xhigh", "claude-haiku-5-5", "xhigh", nil,
			`thinking={"type":"adaptive"} output_config={"effort":"xhigh"} tool_choice=-`, nil},
		{"opus 4.6 xhigh", "claude-opus-4-6", "xhigh", nil,
			`thinking={"type":"adaptive"} output_config={"effort":"max"} tool_choice=-`, []string{"approximated output_config.effort"}},
		{"opus 4.8 xhigh", "claude-opus-4-8", "xhigh", nil,
			`thinking={"type":"adaptive"} output_config={"effort":"xhigh"} tool_choice=-`, nil},
		{"sonnet 4.5 high, forced tool", "claude-sonnet-4-5", "high", &llm.ToolChoice{Mode: llm.ToolChoiceRequired},
			`thinking=- output_config=- tool_choice={"type":"any"}`, []string{"dropped thinking"}},
	} {
		req := chatRequest(c.model, chatText(llm.RoleUser, "Hi", 0))
		req.Options.ReasoningEffort = c.effort
		req.Options.ToolChoice = c.choice
		req.Tools = []llm.Tool{tool}
		body, tr, err := buildBody(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := fields(body, "thinking", "output_config", "tool_choice"); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		if got := lossPaths(tr); !slices.Equal(got, c.losses) {
			t.Errorf("%s: losses %q, want %q", c.name, got, c.losses)
		}
	}
}

// A Messages client's own body gets the same thinking repairs: thinking off sent as each model
// takes it, a forced tool choice and top_k where the model refuses them, and a budget-thinking
// tool turn that has no thinking block. Its thinking blocks and signatures are sent unchanged,
// empty ones included.
func TestBuildBodyFitsNativeThinking(t *testing.T) {
	const signed = `{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"CAISBAoCEAE="},{"type":"text","text":"b"}]},{"role":"user","content":"c"}`
	for _, c := range []struct {
		name, model, in, want string
		losses                []string
	}{
		{"fable disabled", "claude-fable-5-2",
			`{"model":"claude-fable-5-2","max_tokens":100,"thinking":{"type":"disabled"},"top_k":5,"messages":[` + signed + `]}`,
			`{"model":"claude-fable-5-2","max_tokens":100,"output_config":{"effort":"low"},"messages":[` + signed + `]}`,
			[]string{"approximated thinking", "dropped top_k"}},
		{"sonnet 5.5 disabled, forced tool", "claude-sonnet-5-5",
			`{"model":"claude-sonnet-5-5","max_tokens":100,"thinking":{"type":"disabled"},"tools":[{"name":"w","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"},"messages":[` + signed + `]}`,
			`{"model":"claude-sonnet-5-5","max_tokens":100,"thinking":{"type":"between_tools"},"tools":[{"name":"w","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"},"messages":[` + signed + `]}`,
			[]string{"approximated thinking.type", "approximated tool_choice"}},
		{"sonnet 4.5 budget tool turn", "claude-sonnet-4-5",
			`{"model":"claude-sonnet-4-5","max_tokens":10000,"thinking":{"type":"enabled","budget_tokens":4000},"messages":[{"role":"user","content":"w?"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"w","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`,
			`{"model":"claude-sonnet-4-5","max_tokens":10000,"messages":[{"role":"user","content":"w?"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"w","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`,
			[]string{"dropped thinking"}},
		{"opus 4.8 signed history kept", "claude-opus-4-8",
			`{"model":"claude-opus-4-8","max_tokens":100,"thinking":{"type":"adaptive"},"messages":[` + signed + `]}`,
			`{"model":"claude-opus-4-8","max_tokens":100,"thinking":{"type":"adaptive"},"messages":[` + signed + `]}`,
			nil},
	} {
		body, tr, err := buildBody(llm.Request{Model: c.model, Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(c.in)}})
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		_ = json.Unmarshal(body, &got)
		_ = json.Unmarshal([]byte(c.want), &want)
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, g, w)
		}
		if got := lossPaths(tr); !slices.Equal(got, c.losses) {
			t.Errorf("%s: losses %q, want %q", c.name, got, c.losses)
		}
	}
}

// A Responses client's reasoning.summary reaches Claude as thinking.display, and is no longer
// reported as dropped.
func TestBuildBodyMapsReasoningSummary(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{"model":"claude-opus-4-8","input":"hi","reasoning":{"effort":"high","summary":"auto"}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := fields(body, "thinking"); got != `thinking={"display":"summarized","type":"adaptive"}` {
		t.Errorf("%s, want display summarized", got)
	}
	if got := lossPaths(tr); slices.Contains(got, "dropped reasoning") {
		t.Errorf("losses %q: reasoning.summary is still reported dropped", got)
	}
}
