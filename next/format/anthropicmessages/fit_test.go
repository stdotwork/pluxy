package anthropicmessages

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A reasoning effort becomes the thinking settings each model takes: a level the model has as
// that level, another as the nearest one it has, and none as the model's way of not thinking, with
// each difference from what was asked reported. One row per model family.
func TestApplyEffortPerModel(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		want          string // thinking and output_config as JSON; "{}" for neither
		losses        []string
	}{
		// Fable and Mythos: every level, thinking always on.
		{"claude-fable-5-2", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-mythos-5-1", "max", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, nil},
		{"claude-fable-5-2", "none", `{"output_config":{"effort":"low"}}`, []string{"approximated thinking"}},
		{"claude-mythos-5", "none", `{"output_config":{"effort":"low"}}`, []string{"approximated thinking"}},
		// Opus 5.5: thinking always on.
		{"claude-opus-5-5", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-opus-5-5", "none", `{"output_config":{"effort":"low"}}`, []string{"approximated thinking"}},
		// Opus 5: thinking off is taken.
		{"claude-opus-5", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-opus-5", "none", `{"thinking":{"type":"disabled"}}`, nil},
		// Sonnet 5.5: between_tools in place of disabled.
		{"claude-sonnet-5-5-20261001", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-sonnet-5-5", "none", `{"thinking":{"type":"between_tools"}}`, []string{"approximated thinking.type"}},
		// Haiku 5.5.
		{"claude-haiku-5-5", "xhigh", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, nil},
		{"claude-haiku-5-5", "none", `{"thinking":{"type":"disabled"}}`, nil},
		// Claude 4.x: Opus 4.7 and 4.8 have xhigh, Opus and Sonnet 4.6 do not; older models take a budget.
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

// fitCase runs FitThinking on a Messages body for model and checks the body and the losses.
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

// Thinking settings a model refuses are sent in a form it takes, per Anthropic's per-model table
// (https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting).
func TestFitThinkingTypes(t *testing.T) {
	// Fable and Mythos always think: disabled is left out, at the lowest effort unless one is set.
	fitCase(t, "fable disabled", "claude-fable-5-2",
		`{"model":"claude-fable-5-2","thinking":{"type":"disabled"},`+userHi+`}`,
		`{"model":"claude-fable-5-2","output_config":{"effort":"low"},`+userHi+`}`, "approximated thinking")
	fitCase(t, "mythos disabled at high", "claude-mythos-5-2",
		`{"model":"claude-mythos-5-2","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		`{"model":"claude-mythos-5-2","output_config":{"effort":"high"},`+userHi+`}`, "approximated thinking")
	// Opus 5.5: the same, and a budget becomes adaptive thinking at the effort it falls in.
	fitCase(t, "opus 5.5 between_tools", "claude-opus-5-5",
		`{"model":"claude-opus-5-5","thinking":{"type":"between_tools"},`+userHi+`}`,
		`{"model":"claude-opus-5-5","output_config":{"effort":"low"},`+userHi+`}`, "approximated thinking")
	fitCase(t, "opus 5.5 budget", "claude-opus-5-5",
		`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":16000,"display":"summarized"},`+userHi+`}`,
		`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated thinking.budget_tokens")
	// Opus 5: disabled only at high effort or below; display has no place with it.
	fitCase(t, "opus 5 disabled at max", "claude-opus-5",
		`{"model":"claude-opus-5","thinking":{"type":"disabled","display":"omitted"},"output_config":{"effort":"max"},`+userHi+`}`,
		`{"model":"claude-opus-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated output_config.effort", "dropped thinking.display")
	fitCase(t, "opus 5 disabled at high", "claude-opus-5",
		`{"model":"claude-opus-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		`{"model":"claude-opus-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`)
	// Sonnet 5.5: between_tools in place of disabled, with no other field, at high effort or below.
	fitCase(t, "sonnet 5.5 disabled", "claude-sonnet-5-5",
		`{"model":"claude-sonnet-5-5","thinking":{"type":"disabled"},`+userHi+`}`,
		`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools"},`+userHi+`}`, "approximated thinking.type")
	fitCase(t, "sonnet 5.5 between_tools at xhigh", "claude-sonnet-5-5",
		`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools","display":"summarized"},"output_config":{"effort":"xhigh"},`+userHi+`}`,
		`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated output_config.effort", "dropped thinking.display")
	// Haiku 5.5: disabled at high effort or below; between_tools is Sonnet 5.5's alone.
	fitCase(t, "haiku 5.5 between_tools at max", "claude-haiku-5-5",
		`{"model":"claude-haiku-5-5","thinking":{"type":"between_tools"},"output_config":{"effort":"max"},`+userHi+`}`,
		`{"model":"claude-haiku-5-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"},`+userHi+`}`,
		"approximated output_config.effort", "approximated thinking.type")
	// Claude 4.x: Opus 4.6 has no xhigh; Opus 4.8 has no budget; Sonnet 4.5 takes all of these as sent.
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
	// A name the parser cannot place is left alone.
	fitCase(t, "unplaced", "claude-mythos-preview",
		`{"model":"claude-mythos-preview","thinking":{"type":"disabled"},`+userHi+`}`,
		`{"model":"claude-mythos-preview","thinking":{"type":"disabled"},`+userHi+`}`)
}

const oneTool = `"tools":[{"name":"w","input_schema":{"type":"object"}}]`

// A forced tool choice goes to a model that refuses it as auto, and a budget-thinking request
// that forces a tool goes without thinking; top_k is left out where Claude refuses it.
// (https://platform.claude.com/docs/en/build-with-claude/thinking, "Response prefill and forced
// tool use" and "Sampling parameters".)
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
}

// The conversation is repaired for thinking: an assistant turn in the middle of a tool call with
// no thinking block first, while budget thinking is on, and a conversation that ends on an
// assistant turn while thinking is on or on reasoning while it is off, on the models that take
// a prefilled turn at all (Claude 4.6 and later refuse it, which RepairTurns handles).
func TestFitThinkingHistory(t *testing.T) {
	const toolTurn = `{"role":"user","content":"Weather?"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"w","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"sunny"}]}`
	fitCase(t, "sonnet 4.5 tool turn without thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+toolTurn+`]}`,
		`{"model":"claude-sonnet-4-5","messages":[`+toolTurn+`]}`, "dropped thinking")
	fitCase(t, "opus 4.6 tool turn without thinking", "claude-opus-4-6",
		`{"model":"claude-opus-4-6","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+toolTurn+`]}`,
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"messages":[`+toolTurn+`]}`,
		"approximated thinking")
	// Adaptive thinking needs no thinking block first.
	fitCase(t, "opus 4.6 adaptive tool turn", "claude-opus-4-6",
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"messages":[`+toolTurn+`]}`,
		`{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},"messages":[`+toolTurn+`]}`)
	// An earlier turn that thought keeps thinking on, as LiteLLM does.
	const thought = `{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"EqoB"},{"type":"text","text":"b"}]},`
	fitCase(t, "sonnet 4.5 earlier thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+thought+toolTurn+`]}`,
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[`+thought+toolTurn+`]}`)
	// A prefilled turn while thinking is on.
	fitCase(t, "sonnet 4.5 prefill with thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hel"}]}`,
		`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":4000},`+userHi+`}`, "dropped messages[1]")
	fitCase(t, "sonnet 4.5 prefill without thinking", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hel"}]}`,
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hel"}]}`)
	// A conversation that ends on reasoning, with thinking off: the trailing reasoning goes.
	fitCase(t, "sonnet 4.5 ends on reasoning", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"text","text":"Hel"},{"type":"thinking","thinking":"x","signature":"EqoB"},{"type":"redacted_thinking","data":"y"}]}]}`,
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"text","text":"Hel"}]}]}`,
		"dropped messages[1].content[1]", "dropped messages[1].content[2]")
	fitCase(t, "sonnet 4.5 only reasoning", "claude-sonnet-4-5",
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"EqoB"}]}]}`,
		`{"model":"claude-sonnet-4-5",`+userHi+`}`, "dropped messages[1]")
}

// A Responses client's reasoning.summary becomes Claude's thinking display: auto, concise and
// detailed as summarized, none as omitted. A Chat client cannot ask for one.
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

// Claude signatures in the current CAIS envelope (base64 starting with C: a protobuf whose first
// field is a varint, then field 2) are replayed from a Responses reasoning item like the older E
// and R forms, and so is a signed block with no text, as Claude 5 models send with display
// omitted. OpenAI's own encrypted content is still left out.
func TestReplayedCAISSignature(t *testing.T) {
	// 0x08 0x02 0x12 ...: field 1 varint 2, field 2 bytes.
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
