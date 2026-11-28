package anthropicmessages

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestApplyEffortPerModel(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		want          string
		losses        []string
	}{
		{"claude-fable-5-2", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-mythos-5-1", "max", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, nil},
		{"claude-fable-5-2", "none", `{"output_config":{"effort":"low"}}`, []string{"approximated thinking"}},
		{"claude-mythos-5", "none", `{"output_config":{"effort":"low"}}`, []string{"approximated thinking"}},
		{"claude-opus-5-5", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-opus-5-5", "none", `{"output_config":{"effort":"low"}}`, []string{"approximated thinking"}},
		{"claude-opus-5", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-opus-5", "none", `{"thinking":{"type":"disabled"}}`, nil},
		{"claude-sonnet-5-5-20261001", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-sonnet-5-5", "none", `{"thinking":{"type":"between_tools"}}`, []string{"approximated thinking.type"}},
		{"claude-haiku-5-5", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-haiku-5-5", "none", `{"thinking":{"type":"disabled"}}`, nil},
		{"claude-opus-4-8", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-opus-4-6", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, []string{"approximated output_config.effort"}},
		{"claude-sonnet-4-6", "minimal", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`, []string{"approximated output_config.effort"}},
		{"claude-opus-4-7", "none", `{"thinking":{"type":"disabled"}}`, nil},
		{"claude-sonnet-4-5", "xhigh", `{"thinking":{"type":"enabled","budget_tokens":31999}}`, nil},
	} {
		body := []byte(`{"model":"` + c.model + `","max_tokens":32000}`)
		out, losses, applied, err := ApplyEffortReport(body, c.model, c.effort)
		if err != nil {
			t.Fatal(err)
		}
		if !applied {
			t.Errorf("%s %s: not applied", c.model, c.effort)
			continue
		}
		got := map[string]json.RawMessage{}
		_ = json.Unmarshal(out, &got)
		delete(got, "model")
		delete(got, "max_tokens")
		g, _ := json.Marshal(got)
		sameJSON(t, c.model+" "+c.effort, g, c.want)
		if l := lossList(losses); !slices.Equal(l, c.losses) {
			t.Errorf("%s %s: losses %q, want %q", c.model, c.effort, l, c.losses)
		}
	}
}

func fitCase(t *testing.T, name, model, body, want string, losses ...string) {
	t.Helper()
	out, got, err := FitThinking([]byte(body), model)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	sameJSON(t, name, out, want)
	if l := lossList(got); !slices.Equal(l, losses) {
		t.Errorf("%s: losses %q, want %q", name, l, losses)
	}
	if len(losses) == 0 && string(out) != body {
		t.Errorf("%s: a body that needs nothing came back changed: %s", name, out)
	}
}

const userHi = `"messages":[{"role":"user","content":"Hi"}]`

func TestFitThinkingTypes(t *testing.T) {
	fitCase(t, "fable disabled", "claude-fable-5-2",
		`{"model":"claude-fable-5-2","thinking":{"type":"disabled"},`+userHi+`}`,
		`{"model":"claude-fable-5-2","output_config":{"effort":"low"},`+userHi+`}`, "approximated thinking")
	fitCase(t, "mythos disabled at high", "claude-mythos-5-2",
		`{"model":"claude-mythos-5-2","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		`{"model":"claude-mythos-5-2","output_config":{"effort":"high"},`+userHi+`}`, "approximated thinking")
	fitCase(t, "opus 5.5 between_tools", "claude-opus-5-5",
		`{"model":"claude-opus-5-5","thinking":{"type":"between_tools"},`+userHi+`}`,
		`{"model":"claude-opus-5-5","output_config":{"effort":"low"},`+userHi+`}`, "approximated thinking")
	fitCase(t, "opus 5.5 budget", "claude-opus-5-5",
		`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":16000,"display":"summarized"},`+userHi+`}`,
		`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated thinking.budget_tokens")
	fitCase(t, "opus 5 disabled at max", "claude-opus-5",
		`{"model":"claude-opus-5","thinking":{"type":"disabled","display":"omitted"},"output_config":{"effort":"max"},`+userHi+`}`,
		`{"model":"claude-opus-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated output_config.effort", "dropped thinking.display")
	fitCase(t, "opus 5 disabled at high", "claude-opus-5",
		`{"model":"claude-opus-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		`{"model":"claude-opus-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`)
	fitCase(t, "sonnet 5.5 disabled", "claude-sonnet-5-5",
		`{"model":"claude-sonnet-5-5","thinking":{"type":"disabled"},`+userHi+`}`,
		`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools"},`+userHi+`}`, "approximated thinking.type")
	fitCase(t, "sonnet 5.5 between_tools at xhigh", "claude-sonnet-5-5",
		`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools","display":"summarized"},"output_config":{"effort":"xhigh"},`+userHi+`}`,
		`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated output_config.effort", "dropped thinking.display")
	fitCase(t, "haiku 5.5 between_tools at max", "claude-haiku-5-5",
		`{"model":"claude-haiku-5-5","thinking":{"type":"between_tools"},"output_config":{"effort":"max"},`+userHi+`}`,
		`{"model":"claude-haiku-5-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated output_config.effort", "approximated thinking.type")
	fitCase(t, "opus 4.6 xhigh", "claude-opus-4-6",
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"},`+userHi+`}`,
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"output_config":{"effort":"max"},`+userHi+`}`,
		"approximated output_config.effort")
	fitCase(t, "opus 4.8 budget", "claude-opus-4-8",
		`{"model":"claude-opus-4-8","thinking":{"type":"enabled","budget_tokens":2000},`+userHi+`}`,
		`{"model":"claude-opus-4-8","thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},`+userHi+`}`,
		"approximated thinking.budget_tokens")
	fitCase(t, "sonnet 4.5 budget", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":2000},`+userHi+`}`,
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":2000},`+userHi+`}`)
	fitCase(t, "sonnet 4.5 disabled with display", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"disabled","display":"summarized"},`+userHi+`}`,
		`{"model":"claude-sonnet-4-5","thinking":{"type":"disabled"},`+userHi+`}`, "dropped thinking.display")
	fitCase(t, "sonnet 4.5 adaptive", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","max_tokens":8000,"thinking":{"type":"adaptive"},`+userHi+`}`,
		`{"model":"claude-sonnet-4-5","max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":1024},`+userHi+`}`,
		"approximated thinking.type")
	fitCase(t, "opus 4.5 adaptive with effort", "claude-opus-4-5",
		`{"model":"claude-opus-4-5","max_tokens":32000,"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"medium"},`+userHi+`}`,
		`{"model":"claude-opus-4-5","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":8192,"display":"summarized"},"output_config":{"effort":"medium"},`+userHi+`}`,
		"approximated thinking.type")
	fitCase(t, "haiku 4.5 adaptive clamped", "claude-haiku-4-5",
		`{"model":"claude-haiku-4-5","max_tokens":4096,"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},`+userHi+`}`,
		`{"model":"claude-haiku-4-5","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":4095},`+userHi+`}`,
		"approximated output_config.effort", "approximated thinking.type")
	fitCase(t, "sonnet 4.5 adaptive small max_tokens", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","max_tokens":1024,"thinking":{"type":"adaptive"},`+userHi+`}`,
		`{"model":"claude-sonnet-4-5","max_tokens":1024,`+userHi+`}`,
		"dropped thinking")
	fitCase(t, "opus 4.6 adaptive unchanged", "claude-opus-4-6",
		`{"model":"claude-opus-4-6","max_tokens":8000,"thinking":{"type":"adaptive"},`+userHi+`}`,
		`{"model":"claude-opus-4-6","max_tokens":8000,"thinking":{"type":"adaptive"},`+userHi+`}`)
	fitCase(t, "mythos preview disabled", "claude-mythos-preview",
		`{"model":"claude-mythos-preview","thinking":{"type":"disabled"},`+userHi+`}`,
		`{"model":"claude-mythos-preview","output_config":{"effort":"low"},`+userHi+`}`, "approximated thinking")
	fitCase(t, "mythos preview xhigh", "claude-mythos-preview",
		`{"model":"claude-mythos-preview","thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"},`+userHi+`}`,
		`{"model":"claude-mythos-preview","thinking":{"type":"adaptive"},"output_config":{"effort":"max"},`+userHi+`}`,
		"approximated output_config.effort")
	fitCase(t, "mythos preview budget unchanged", "claude-mythos-preview",
		`{"model":"claude-mythos-preview","thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`,
		`{"model":"claude-mythos-preview","thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`)
	fitCase(t, "unplaced", "claude-unknown-preview",
		`{"model":"claude-unknown-preview","thinking":{"type":"disabled"},`+userHi+`}`,
		`{"model":"claude-unknown-preview","thinking":{"type":"disabled"},`+userHi+`}`)
}

const oneTool = `"tools":[{"name":"w","input_schema":{"type":"object"}}]`

func TestFitThinkingToolChoiceAndTopK(t *testing.T) {
	fitCase(t, "fable 5.2 any", "claude-fable-5-2",
		`{"model":"claude-fable-5-2",`+oneTool+`,"tool_choice":{"type":"any","disable_parallel_tool_use":true},`+userHi+`}`,
		`{"model":"claude-fable-5-2",`+oneTool+`,"tool_choice":{"type":"auto","disable_parallel_tool_use":true},`+userHi+`}`,
		"approximated tool_choice")
	fitCase(t, "fable 5 any", "claude-fable-5",
		`{"model":"claude-fable-5",`+oneTool+`,"tool_choice":{"type":"any"},`+userHi+`}`,
		`{"model":"claude-fable-5",`+oneTool+`,"tool_choice":{"type":"any"},`+userHi+`}`)
	fitCase(t, "opus 5.5 named tool", "claude-opus-5-5",
		`{"model":"claude-opus-5-5",`+oneTool+`,"tool_choice":{"type":"tool","name":"w"},`+userHi+`}`,
		`{"model":"claude-opus-5-5",`+oneTool+`,"tool_choice":{"type":"auto"},`+userHi+`}`,
		"approximated tool_choice")
	fitCase(t, "sonnet 5.5 any", "claude-sonnet-5-5",
		`{"model":"claude-sonnet-5-5",`+oneTool+`,"tool_choice":{"type":"any"},`+userHi+`}`,
		`{"model":"claude-sonnet-5-5",`+oneTool+`,"tool_choice":{"type":"auto"},`+userHi+`}`,
		"approximated tool_choice")
	fitCase(t, "opus 5 any adaptive", "claude-opus-5",
		`{"model":"claude-opus-5",`+oneTool+`,"tool_choice":{"type":"any"},"thinking":{"type":"adaptive"},`+userHi+`}`,
		`{"model":"claude-opus-5",`+oneTool+`,"tool_choice":{"type":"any"},"thinking":{"type":"adaptive"},`+userHi+`}`)
	fitCase(t, "haiku 5.5 top_k", "claude-haiku-5-5",
		`{"model":"claude-haiku-5-5","top_k":40,`+userHi+`}`,
		`{"model":"claude-haiku-5-5",`+userHi+`}`, "dropped top_k")
	fitCase(t, "opus 4.6 budget any", "claude-opus-4-6",
		`{"model":"claude-opus-4-6",`+oneTool+`,"tool_choice":{"type":"any"},"thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`,
		`{"model":"claude-opus-4-6",`+oneTool+`,"tool_choice":{"type":"any"},`+userHi+`}`, "dropped thinking")
	fitCase(t, "sonnet 4.5 budget top_k", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","top_k":40,"thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`,
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`, "dropped top_k")
	fitCase(t, "sonnet 4.5 top_k without thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","top_k":40,`+userHi+`}`,
		`{"model":"claude-sonnet-4-5","top_k":40,`+userHi+`}`)
	fitCase(t, "mythos preview top_k", "claude-mythos-preview",
		`{"model":"claude-mythos-preview","top_k":40,`+userHi+`}`,
		`{"model":"claude-mythos-preview",`+userHi+`}`, "dropped top_k")
	fitCase(t, "mythos preview any adaptive", "claude-mythos-preview",
		`{"model":"claude-mythos-preview",`+oneTool+`,"tool_choice":{"type":"any"},"thinking":{"type":"adaptive"},`+userHi+`}`,
		`{"model":"claude-mythos-preview",`+oneTool+`,"tool_choice":{"type":"any"},"thinking":{"type":"adaptive"},`+userHi+`}`)
	fitCase(t, "mythos preview any budget", "claude-mythos-preview",
		`{"model":"claude-mythos-preview",`+oneTool+`,"tool_choice":{"type":"any"},"thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`,
		`{"model":"claude-mythos-preview",`+oneTool+`,"tool_choice":{"type":"any"},`+userHi+`}`, "dropped thinking")
}

func TestFitThinkingHistory(t *testing.T) {
	const toolTurn = `{"role":"user","content":"Weather?"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"w","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"sunny"}]}`
	fitCase(t, "sonnet 4.5 tool turn without thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+toolTurn+`]}`,
		`{"model":"claude-sonnet-4-5","messages":[`+toolTurn+`]}`, "dropped thinking")
	fitCase(t, "opus 4.6 tool turn without thinking", "claude-opus-4-6",
		`{"model":"claude-opus-4-6","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+toolTurn+`]}`,
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"messages":[`+toolTurn+`]}`,
		"approximated thinking")
	fitCase(t, "opus 4.6 adaptive tool turn", "claude-opus-4-6",
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"messages":[`+toolTurn+`]}`,
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"messages":[`+toolTurn+`]}`)
	const thought = `{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"EqoB"},{"type":"text","text":"b"}]},`
	fitCase(t, "sonnet 4.5 earlier thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+thought+toolTurn+`]}`,
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+thought+toolTurn+`]}`)
	fitCase(t, "sonnet 4.5 prefill with thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hel"}]}`,
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`, "dropped messages[1]")
	fitCase(t, "sonnet 4.5 prefill without thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hel"}]}`,
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hel"}]}`)
	fitCase(t, "sonnet 4.5 ends on reasoning", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"text","text":"Hel"},{"type":"thinking","thinking":"x","signature":"EqoB"},{"type":"redacted_thinking","data":"y"}]}]}`,
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"text","text":"Hel"}]}]}`,
		"dropped messages[1].content[1]", "dropped messages[1].content[2]")
	fitCase(t, "sonnet 4.5 only reasoning", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"EqoB"}]}]}`,
		`{"model":"claude-sonnet-4-5",`+userHi+`}`, "dropped messages[1]")
}

func TestApplySummary(t *testing.T) {
	for _, c := range []struct {
		name, model, request, body, want string
		mapped                           bool
		losses                           []string
	}{
		{"auto", "claude-opus-4-8", `{"model":"m","input":"hi","reasoning":{"effort":"high","summary":"auto"}}`,
			`{"thinking":{"type":"adaptive"}}`, `{"thinking":{"type":"adaptive","display":"summarized"}}`, true, nil},
		{"detailed", "claude-opus-4-6", `{"model":"m","input":"hi","reasoning":{"effort":"high","summary":"detailed"}}`,
			`{"thinking":{"type":"adaptive"}}`, `{"thinking":{"type":"adaptive","display":"summarized"}}`, true, []string{"approximated reasoning.summary"}},
		{"none", "claude-opus-4-6", `{"model":"m","input":"hi","reasoning":{"effort":"high","summary":"none"}}`,
			`{"thinking":{"type":"adaptive"}}`, `{"thinking":{"type":"adaptive","display":"omitted"}}`, true, nil},
		{"auto, thinking on by default", "claude-fable-5-2", `{"model":"m","input":"hi","reasoning":{"summary":"auto"}}`,
			`{}`, `{"thinking":{"type":"adaptive","display":"summarized"}}`, true, nil},
		{"auto, mythos preview thinking on by default", "claude-mythos-preview", `{"model":"m","input":"hi","reasoning":{"summary":"auto"}}`,
			`{}`, `{"thinking":{"type":"adaptive","display":"summarized"}}`, true, nil},
		{"auto, thinking off", "claude-sonnet-5-5", `{"model":"m","input":"hi","reasoning":{"effort":"none","summary":"auto"}}`,
			`{"thinking":{"type":"between_tools"}}`, `{"thinking":{"type":"between_tools"}}`, false, nil},
		{"budget model", "claude-sonnet-4-5", `{"model":"m","input":"hi","reasoning":{"effort":"high","summary":"concise"}}`,
			`{"thinking":{"type":"enabled","budget_tokens":24576}}`, `{"thinking":{"type":"enabled","budget_tokens":24576,"display":"summarized"}}`, true, []string{"approximated reasoning.summary"}},
		{"no summary", "claude-opus-4-8", `{"model":"m","input":"hi","reasoning":{"effort":"high"}}`,
			`{"thinking":{"type":"adaptive"}}`, `{"thinking":{"type":"adaptive"}}`, false, nil},
	} {
		req, err := openairesponses.ParseRequest([]byte(c.request))
		if err != nil {
			t.Fatal(err)
		}
		out, losses, mapped, err := ApplySummary([]byte(c.body), c.model, req)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, c.name, out, c.want)
		if mapped != c.mapped {
			t.Errorf("%s: mapped %v, want %v", c.name, mapped, c.mapped)
		}
		if l := lossList(losses); !slices.Equal(l, c.losses) {
			t.Errorf("%s: losses %q, want %q", c.name, l, c.losses)
		}
	}
	chat := llm.Request{Model: "m", Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)}}
	out, _, mapped, err := ApplySummary([]byte(`{"thinking":{"type":"adaptive"}}`), "claude-opus-4-8", chat)
	if err != nil || mapped || string(out) != `{"thinking":{"type":"adaptive"}}` {
		t.Errorf("Chat request: mapped %v, body %s, err %v; want it left alone", mapped, out, err)
	}
}

func TestReplayedCAISSignature(t *testing.T) {
	const cais = "CAISBAoCEAE="
	replay, err := openairesponses.ParseRequest([]byte(`{"model":"m","input":[
	 {"type":"message","role":"user","content":"Weather?"},
	 {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"` + cais + `"},
	 {"type":"function_call","call_id":"c1","name":"w","arguments":"{}"},
	 {"type":"function_call_output","call_id":"c1","output":"sunny"},
	 {"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"Cgarbage"},
	 {"type":"reasoning","id":"rs_3","summary":[],"encrypted_content":"gAAAAABopenai"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, losses, err := EncodeRequest(replay)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "body", encoded, `{"model":"m","stream":false,"max_tokens":32000,"messages":[
	 {"role":"user","content":[{"type":"text","text":"Weather?"}]},
	 {"role":"assistant","content":[
	  {"type":"thinking","thinking":"","signature":"`+cais+`"},
	  {"type":"tool_use","id":"c1","name":"w","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"sunny"}]}]}`)
	got := strings.Join(lossList(losses), ",")
	if !strings.Contains(got, "dropped input[4]") || !strings.Contains(got, "dropped input[5]") || strings.Contains(got, "input[1]") {
		t.Errorf("losses %q: want input[4] and input[5] dropped and input[1] kept", got)
	}
}

func TestFitAdaptiveEffortOnBudgetOnlyModels(t *testing.T) {
	const body = `{"model":"m","max_tokens":40000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`
	for model, keep := range map[string]bool{
		"claude-sonnet-4-5":         false,
		"claude-haiku-4-5-20251001": false,
		"claude-opus-4-1":           false,
		"claude-opus-4-5-20251101":  true,
	} {
		t.Run(model, func(t *testing.T) {
			out, losses, err := FitThinking([]byte(body), model)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Thinking struct {
					Type   string `json:"type"`
					Budget int    `json:"budget_tokens"`
				} `json:"thinking"`
				OutputConfig map[string]json.RawMessage `json:"output_config"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if got.Thinking.Type != "enabled" || got.Thinking.Budget != budgets["high"] {
				t.Errorf("thinking = %+v", got.Thinking)
			}
			if _, has := got.OutputConfig["effort"]; has != keep {
				t.Errorf("effort kept = %v, want %v: %s", has, keep, out)
			}
			reported := false
			for _, l := range losses {
				reported = reported || l.Path == "output_config.effort"
			}
			if reported == keep {
				t.Errorf("effort loss reported = %v with kept = %v: %+v", reported, keep, losses)
			}
		})
	}
}
