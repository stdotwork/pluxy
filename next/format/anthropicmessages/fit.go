package anthropicmessages

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// Claude models differ in the thinking settings they take, and answer the others with a 400.
// thinkingRules is what a model takes, as Anthropic's tables give it:
//   - thinking types and their defaults, per model:
//     https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting ("Thinking
//     support, defaults, and rejected configurations by model");
//   - effort levels: https://platform.claude.com/docs/en/build-with-claude/effort (max on every
//     model that thinks adaptively, xhigh from Claude 4.7: "Not every model that supports max
//     supports xhigh");
//   - forced tool choice, top_k and display:
//     https://platform.claude.com/docs/en/build-with-claude/thinking ("Response prefill and forced
//     tool use", "Sampling parameters", "Controlling thinking display").
//
// shortcut: decided from the model name, as claudeVersion is. Move to the model's own
// capabilities (Anthropic's model list has capabilities.effort and capabilities.thinking.types)
// when the core's model list carries them.
type thinkingRules struct {
	adaptive        bool   // takes thinking type adaptive: Claude 4.6 and later, Mythos Preview
	budget          bool   // takes thinking type enabled with budget_tokens: Claude 4.6 and earlier, Mythos Preview
	xhigh           bool   // takes effort xhigh: Claude 4.7 and later
	newSampling     bool   // refuses top_k on every request: Claude 4.7 and later, Mythos Preview
	thinksByDefault bool   // thinks when the request has no thinking setting
	off             string // the thinking type that turns thinking off; "" where thinking is always on
	offUpToHigh     bool   // off is taken only at effort high or below
	noForcedTools   bool   // refuses tool_choice any and tool on every request
}

// rulesFor returns the thinking rules for model; ok is false for a name that is not a Claude
// model that can think (claudeVersion, plus Claude Mythos Preview), which is then left as it is.
func rulesFor(model string) (r thinkingRules, ok bool) {
	name := claudeName(model)
	if name == "claude-mythos-preview" || strings.HasPrefix(name, "claude-mythos-preview-") {
		return thinkingRules{
			adaptive:        true,
			budget:          true,
			newSampling:     true,
			thinksByDefault: true,
		}, true
	}
	major, minor, ok := claudeVersion(model)
	if !ok {
		return r, false
	}
	if !adaptive(major, minor) {
		return thinkingRules{budget: true, off: "disabled"}, true
	}
	from := func(ma, mi int) bool { return major > ma || (major == ma && minor >= mi) }
	r = thinkingRules{adaptive: true, budget: !from(4, 7), xhigh: from(4, 7), newSampling: from(4, 7), off: "disabled"}
	family := ""
	if f := claudeFamily.FindStringSubmatch(name); f != nil {
		family = f[1]
	}
	switch family {
	case "fable", "mythos":
		// Always on; Fable and Mythos 5.1 and later refuse a forced tool choice.
		r.thinksByDefault, r.off, r.noForcedTools = true, "", from(5, 1)
	case "opus":
		r.thinksByDefault = from(5, 0)
		switch {
		case from(5, 5):
			r.off, r.noForcedTools = "", true
		case from(5, 0):
			r.offUpToHigh = true
		}
	case "sonnet":
		r.thinksByDefault = from(5, 0)
		if from(5, 5) {
			r.off, r.offUpToHigh, r.noForcedTools = "between_tools", true, true
		}
	case "haiku":
		r.thinksByDefault, r.offUpToHigh = from(5, 5), from(5, 5)
	}
	return r, true
}

// fitter changes a Messages body's top-level fields to ones its model takes, recording each change.
type fitter struct {
	m      map[string]json.RawMessage
	r      thinkingRules
	losses []llm.Loss
}

func (f *fitter) lose(kind, path, detail string) {
	f.losses = llm.AddLoss(f.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

func (f *fitter) set(key string, v any) {
	b, _ := json.Marshal(v)
	f.m[key] = b
}

// thinking returns the body's thinking setting as an object (nil when there is none) and its type.
func (f *fitter) thinking() (map[string]json.RawMessage, string) {
	th := rawjson.Object(f.m["thinking"])
	var t struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(f.m["thinking"], &t)
	return th, t.Type
}

// setThinking sets thinking, or removes it when th is nil.
func (f *fitter) setThinking(th map[string]json.RawMessage) {
	if th == nil {
		delete(f.m, "thinking")
		return
	}
	f.set("thinking", th)
}

func (f *fitter) effort() string {
	var e string
	_ = json.Unmarshal(rawjson.Object(f.m["output_config"])["effort"], &e)
	return e
}

// setEffort sets output_config.effort, keeping the rest of output_config.
func (f *fitter) setEffort(e string) {
	cfg := rawjson.Object(f.m["output_config"])
	if cfg == nil {
		cfg = map[string]json.RawMessage{}
	}
	cfg["effort"], _ = json.Marshal(e)
	f.set("output_config", cfg)
}

// budgetEffort returns the effort a thinking budget falls in, as reasoningEffort reads one.
func budgetEffort(budget int) string {
	for _, b := range effortBudgets {
		if budget <= b.max {
			if b.effort == "minimal" || b.effort == "none" {
				return "low"
			}
			return b.effort
		}
	}
	return "xhigh"
}

// config fits the thinking type and the effort level.
func (f *fitter) config() {
	th, typ := f.thinking()
	switch {
	case typ == "enabled" && f.r.adaptive && !f.r.budget:
		f.toAdaptive(th, "thinking.budget_tokens", "this model takes no thinking budget; sent as adaptive thinking")
	case typ == "adaptive" && !f.r.adaptive && f.r.budget:
		f.toBudget(th)
	case typ == "disabled" || typ == "between_tools":
		f.thinkingOff(th, typ)
	}
	if f.effort() == "xhigh" && f.r.adaptive && !f.r.xhigh {
		f.setEffort("max")
		f.lose(llm.LossApproximated, "output_config.effort", "this model has no effort xhigh; sent as max, the level above it")
	}
}

// toBudget turns adaptive thinking into budget thinking on a model that takes only a budget,
// sized from output_config.effort when set and minBudget otherwise, kept below max_tokens. When
// max_tokens leaves no room for the smallest budget, thinking is left out instead.
func (f *fitter) toBudget(th map[string]json.RawMessage) {
	budget := minBudget
	if b, ok := budgets[f.effort()]; ok {
		budget = b
	}
	var maxTokens *int
	if err := json.Unmarshal(f.m["max_tokens"], &maxTokens); err == nil && maxTokens != nil && budget >= *maxTokens {
		budget = *maxTokens - 1
	}
	if budget < minBudget {
		f.setThinking(nil)
		f.lose(llm.LossDropped, "thinking", "this model takes no adaptive thinking and max_tokens leaves no room for a thinking budget; sent without thinking")
		return
	}
	th["type"] = json.RawMessage(`"enabled"`)
	th["budget_tokens"], _ = json.Marshal(budget)
	f.setThinking(th)
	f.lose(llm.LossApproximated, "thinking.type", "this model takes no adaptive thinking; sent as extended thinking with budget_tokens "+strconv.Itoa(budget))
}

// toAdaptive turns budget thinking into adaptive thinking at the effort the budget falls in,
// unless the body names an effort, and reports it at path.
func (f *fitter) toAdaptive(th map[string]json.RawMessage, path, detail string) {
	var b struct {
		Budget *int `json:"budget_tokens"`
	}
	_ = json.Unmarshal(f.m["thinking"], &b)
	delete(th, "budget_tokens")
	th["type"] = json.RawMessage(`"adaptive"`)
	f.setThinking(th)
	if f.effort() == "" && b.Budget != nil {
		e := budgetEffort(*b.Budget)
		f.setEffort(e)
		detail += " at effort " + e + ", the level the budget falls in"
	}
	f.lose(llm.LossApproximated, path, detail)
}

// thinkingOff sends a request to turn thinking off as the model takes it: without thinking
// settings at the lowest effort where thinking is always on, as between_tools on Claude Sonnet
// 5.5, and with no other field in thinking, which the off types take none of. Where the model
// turns thinking off only at effort high or below, a higher effort is sent as high.
func (f *fitter) thinkingOff(th map[string]json.RawMessage, typ string) {
	if f.r.off == "" {
		f.setThinking(nil)
		detail := "this model always thinks and refuses thinking type " + typ + "; sent without thinking settings"
		if f.effort() == "" {
			f.setEffort("low")
			detail += ", at effort low, the least thinking it does"
		}
		f.lose(llm.LossApproximated, "thinking", detail)
		return
	}
	changed := false
	if typ != f.r.off {
		changed = true
		detail := "this model turns thinking off with thinking type " + f.r.off + ", not " + typ
		if f.r.off == "between_tools" {
			detail += ": no thinking before it answers, short notes between tool calls"
		}
		f.lose(llm.LossApproximated, "thinking.type", detail)
	}
	keys := make([]string, 0, len(th))
	for k := range th {
		if k != "type" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		changed = true
		f.lose(llm.LossDropped, "thinking."+k, "thinking type "+f.r.off+" takes no other field")
	}
	if changed {
		f.set("thinking", map[string]string{"type": f.r.off})
	}
	if e := f.effort(); f.r.offUpToHigh && (e == "xhigh" || e == "max") {
		f.setEffort("high")
		f.lose(llm.LossApproximated, "output_config.effort", "this model turns thinking off only at effort high or below; sent at high")
	}
}

// toolChoice sends a forced tool choice (any, or a named tool) to a model that refuses one as
// auto, and a forced tool choice with budget thinking, which Claude refuses on every model,
// without thinking: a forced call starts the reply, with no thinking before it either way.
func (f *fitter) toolChoice() {
	tc := rawjson.Object(f.m["tool_choice"])
	var c struct {
		Type string `json:"type"`
	}
	if tc == nil || json.Unmarshal(f.m["tool_choice"], &c) != nil || (c.Type != "any" && c.Type != "tool") {
		return
	}
	if f.r.noForcedTools {
		delete(tc, "name")
		tc["type"] = json.RawMessage(`"auto"`)
		f.set("tool_choice", tc)
		f.lose(llm.LossApproximated, "tool_choice", "this model refuses a forced tool choice; sent as auto, so the model may answer without calling a tool")
		return
	}
	if _, typ := f.thinking(); typ == "enabled" {
		f.setThinking(nil)
		f.lose(llm.LossDropped, "thinking", "Claude refuses a forced tool choice with budget thinking; sent without thinking, which a reply that starts with a forced call has none of anyway")
	}
}

// topK leaves out top_k where Claude refuses it: on Claude 4.7 and later and Mythos Preview,
// and on older models while thinking is on.
func (f *fitter) topK() {
	v, ok := f.m["top_k"]
	if !ok || string(v) == "null" {
		return
	}
	switch {
	case f.r.newSampling:
		delete(f.m, "top_k")
		f.lose(llm.LossDropped, "top_k", "this model takes no top_k")
	case thinkingOn(f.m["thinking"]):
		delete(f.m, "top_k")
		f.lose(llm.LossDropped, "top_k", "Claude takes no top_k while thinking is on")
	}
}

// histMessage is one message of a body's conversation.
type histMessage struct {
	role   string
	blocks []json.RawMessage // nil for string content
}

func isThinkingBlock(b json.RawMessage) bool {
	t := blockType(b)
	return t == "thinking" || t == "redacted_thinking"
}

func hasBlock(blocks []json.RawMessage, typ string) bool {
	return slices.ContainsFunc(blocks, func(b json.RawMessage) bool { return blockType(b) == typ })
}

// history repairs the conversation for thinking:
//   - with budget thinking, an assistant turn that a tool result answers must start with a thinking
//     block (Anthropic's Thinking page, "Thinking with tool use": "In extended (manual) mode, the
//     API additionally enforces that the final assistant turn of a thinking-enabled request starts
//     with a thinking block. Adaptive mode relaxes this"). When none of the conversation's
//     assistant turns has thinking, as when a client turned thinking on mid-conversation, the
//     request goes with adaptive thinking on a model that takes it, and else without thinking, as
//     LiteLLM does; a conversation with thinking elsewhere is left as it is;
//   - on the models that take a prefilled assistant turn (Claude 4.5 and earlier; RepairTurns
//     drops it for the others), trailing assistant turns are dropped while thinking is on ("On
//     earlier models, you can't prefill the assistant response while thinking is on"), and with
//     thinking off a prefilled turn that ends on reasoning loses that reasoning, which cannot be
//     continued (thinking may be left out of earlier turns outside tool use), or the whole turn
//     when nothing else is in it.
//
// A conversation with nothing left is an invalid_request error.
func (f *fitter) history(model string) error {
	var raw []json.RawMessage
	if json.Unmarshal(f.m["messages"], &raw) != nil || len(raw) == 0 {
		return nil
	}
	msgs := make([]histMessage, len(raw))
	for i, r := range raw {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(r, &m) != nil {
			return nil
		}
		msgs[i].role = m.Role
		_ = json.Unmarshal(m.Content, &msgs[i].blocks)
	}
	n := len(msgs)
	if th, typ := f.thinking(); typ == "enabled" && n >= 2 {
		last, prev := msgs[n-1], msgs[n-2]
		thought := slices.ContainsFunc(msgs, func(m histMessage) bool {
			return m.role == llm.RoleAssistant && slices.ContainsFunc(m.blocks, isThinkingBlock)
		})
		if last.role == llm.RoleUser && hasBlock(last.blocks, "tool_result") && prev.role == llm.RoleAssistant &&
			hasBlock(prev.blocks, "tool_use") && !thought {
			const why = "the assistant turn this tool result answers has no thinking block, which budget thinking requires first"
			if f.r.adaptive {
				f.toAdaptive(th, "thinking", why+"; sent as adaptive thinking, which does not")
			} else {
				f.setThinking(nil)
				f.lose(llm.LossDropped, "thinking", why+"; sent without thinking")
			}
		}
	}
	if refusesPrefill(model) || msgs[n-1].role != llm.RoleAssistant {
		return nil
	}
	last := msgs[n-1]
	if len(last.blocks) > 0 && blockType(last.blocks[len(last.blocks)-1]) == "server_tool_use" {
		return nil
	}
	if thinkingOn(f.m["thinking"]) {
		for n > 0 && msgs[n-1].role == llm.RoleAssistant {
			n--
			f.lose(llm.LossDropped, "messages["+strconv.Itoa(n)+"]", "Claude refuses a prefilled assistant turn while thinking is on")
		}
	} else {
		k := len(last.blocks)
		for k > 0 && isThinkingBlock(last.blocks[k-1]) {
			k--
		}
		switch {
		case k == len(last.blocks):
			return nil
		case k == 0:
			n--
			f.lose(llm.LossDropped, "messages["+strconv.Itoa(n)+"]", "a prefilled assistant turn with nothing but reasoning, which cannot be continued")
		default:
			for j := k; j < len(last.blocks); j++ {
				f.lose(llm.LossDropped, "messages["+strconv.Itoa(n-1)+"].content["+strconv.Itoa(j)+"]", "reasoning at the end of a prefilled assistant turn, which cannot be continued")
			}
			msg := rawjson.Object(raw[n-1])
			msg["content"], _ = json.Marshal(last.blocks[:k])
			raw[n-1], _ = json.Marshal(msg)
		}
	}
	if !slices.ContainsFunc(msgs[:n], func(m histMessage) bool { return m.role == llm.RoleUser || m.role == llm.RoleAssistant }) {
		return invalid("nothing is left to send once the trailing assistant turn is dropped")
	}
	f.set("messages", raw[:n])
	return nil
}

// FitThinking makes the thinking settings of a Messages body built for model, a client's own or a
// translated one, ones the model takes (thinkingRules), and reports each change with its path in
// body:
//   - a thinking budget on a model that takes none becomes adaptive thinking at the effort the
//     budget falls in;
//   - adaptive thinking on a budget-only model becomes extended thinking with a budget sized from
//     output_config.effort (or minBudget), or is dropped when max_tokens leaves no room;
//   - thinking turned off (disabled, or between_tools) is sent as the model turns it off: without
//     thinking settings at effort low on a model that always thinks, as between_tools on Claude
//     Sonnet 5.5, as disabled elsewhere, with no other field in it, and at effort high at most
//     where the model asks for that;
//   - effort xhigh on a model without it is sent as max;
//   - a forced tool choice is sent as auto to a model that refuses one, and with budget thinking
//     goes without thinking;
//   - top_k is left out where Claude refuses it;
//   - the conversation gets the thinking repairs history describes.
//
// A name that is not a Claude model that can think, and a body that needs none of this, are
// returned as they came.
func FitThinking(body []byte, model string) ([]byte, []llm.Loss, error) {
	r, ok := rulesFor(model)
	if !ok {
		return body, nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, err
	}
	f := &fitter{m: m, r: r}
	f.config()
	f.toolChoice()
	f.topK()
	if err := f.history(model); err != nil {
		return nil, nil, err
	}
	if len(f.losses) == 0 {
		return body, nil, nil
	}
	out, err := json.Marshal(m)
	return out, f.losses, err
}

// ApplySummary sets thinking.display on a Messages body built for model from the reasoning summary
// a Responses client asked for (reasoning.summary, or the older reasoning.generate_summary): auto,
// concise and detailed as summarized (Claude has one summary level, so the last two are reported
// as approximated), none or null as omitted. With no thinking setting in the body, a summary turns
// on adaptive thinking with display summarized on a model that thinks by default anyway; a body
// with thinking off has nothing to summarise. carried reports that the client's reasoning field
// has nothing else in it and is now carried, so the caller stops reporting it as dropped. A
// request in another format, or one that names no summary, is left as it is: Chat Completions
// has no field for it.
func ApplySummary(body []byte, model string, req llm.Request) ([]byte, []llm.Loss, bool, error) {
	o := req.Origin
	if o == nil || o.Format != llm.FormatOpenAIResponses {
		return body, nil, false, nil
	}
	reasoning := rawjson.Object(rawjson.Object(o.Raw)["reasoning"])
	raw, ok := reasoning["summary"]
	if !ok {
		raw, ok = reasoning["generate_summary"]
	}
	r, placed := rulesFor(model)
	if !ok || !placed {
		return body, nil, false, nil
	}
	var summary string
	_ = json.Unmarshal(raw, &summary)
	summary = strings.ToLower(strings.TrimSpace(summary))
	var losses []llm.Loss
	display := "omitted"
	switch summary {
	case "auto":
		display = "summarized"
	case "concise", "detailed":
		display = "summarized"
		losses = append(losses, llm.Loss{Kind: llm.LossApproximated, Path: "reasoning.summary",
			Detail: "Claude has one summary level; sent as thinking display summarized"})
	case "none", "":
	default:
		return body, nil, false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, false, err
	}
	f := &fitter{m: m, r: r}
	th, typ := f.thinking()
	switch {
	case typ == "adaptive" || typ == "enabled":
		th["display"], _ = json.Marshal(display)
		f.setThinking(th)
	case typ == "" && display == "summarized" && r.thinksByDefault:
		f.set("thinking", map[string]string{"type": "adaptive", "display": display})
	case display == "omitted":
		// Thinking is off, or on with its default display, omitted, on every model that thinks by
		// default: nothing to change.
		return body, losses, onlySummary(reasoning), nil
	default:
		return body, nil, false, nil
	}
	out, err := json.Marshal(m)
	return out, losses, onlySummary(reasoning), err
}

// onlySummary reports whether a Responses reasoning field left over from the effort has nothing
// but the summary in it.
func onlySummary(reasoning map[string]json.RawMessage) bool {
	for k := range reasoning {
		if k != "summary" && k != "generate_summary" {
			return false
		}
	}
	return true
}
