package anthropicmessages

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

type thinkingRules struct {
	adaptive        bool
	budget          bool
	noEffort        bool
	xhigh           bool
	newSampling     bool
	thinksByDefault bool
	off             string
	offUpToHigh     bool
	noForcedTools   bool
}

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
		f := claudeFamily.FindStringSubmatch(name)
		opus45 := major == 4 && minor == 5 && f != nil && f[1] == "opus"
		return thinkingRules{budget: true, noEffort: !opus45, off: "disabled"}, true
	}
	from := func(ma, mi int) bool { return major > ma || (major == ma && minor >= mi) }
	r = thinkingRules{adaptive: true, budget: !from(4, 7), xhigh: from(4, 7), newSampling: from(4, 7), off: "disabled"}
	family := ""
	if f := claudeFamily.FindStringSubmatch(name); f != nil {
		family = f[1]
	}
	switch family {
	case "fable", "mythos":
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

func (f *fitter) thinking() (map[string]json.RawMessage, string) {
	th := rawjson.Object(f.m["thinking"])
	var t struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(f.m["thinking"], &t)
	return th, t.Type
}

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

func (f *fitter) setEffort(e string) {
	cfg := rawjson.Object(f.m["output_config"])
	if cfg == nil {
		cfg = map[string]json.RawMessage{}
	}
	cfg["effort"], _ = json.Marshal(e)
	f.set("output_config", cfg)
}

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

func (f *fitter) toBudget(th map[string]json.RawMessage) {
	budget := minBudget
	if b, ok := budgets[f.effort()]; ok {
		budget = b
	}
	var maxTokens *int
	if err := json.Unmarshal(f.m["max_tokens"], &maxTokens); err == nil && maxTokens != nil && budget >= *maxTokens {
		budget = *maxTokens - 1
	}
	if f.r.noEffort && f.effort() != "" {
		cfg := rawjson.Object(f.m["output_config"])
		delete(cfg, "effort")
		if len(cfg) == 0 {
			delete(f.m, "output_config")
		} else {
			f.set("output_config", cfg)
		}
		f.lose(llm.LossApproximated, "output_config.effort", "this model takes no effort; it sets the thinking budget instead")
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

type histMessage struct {
	role   string
	blocks []json.RawMessage
}

func isThinkingBlock(b json.RawMessage) bool {
	t := blockType(b)
	return t == "thinking" || t == "redacted_thinking"
}

func hasBlock(blocks []json.RawMessage, typ string) bool {
	return slices.ContainsFunc(blocks, func(b json.RawMessage) bool { return blockType(b) == typ })
}

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
		return body, losses, onlySummary(reasoning), nil
	default:
		return body, nil, false, nil
	}
	out, err := json.Marshal(m)
	return out, losses, onlySummary(reasoning), err
}

func onlySummary(reasoning map[string]json.RawMessage) bool {
	for k := range reasoning {
		if k != "summary" && k != "generate_summary" {
			return false
		}
	}
	return true
}
