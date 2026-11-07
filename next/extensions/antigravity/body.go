package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// buildBody returns the GenerateContent body for req, as Cloud Code takes it inside its envelope,
// and how it was built. A Gemini client's body is the starting point as it is; any other is
// translated, with the client's reasoning effort as thinking settings on Gemini models. Under the
// reject drop policy a translation that drops anything is refused.
func buildBody(req llm.Request) (map[string]json.RawMessage, *llm.Translation, error) {
	tr := &llm.Translation{To: llm.FormatGeminiGenerate}
	var body []byte
	var losses []llm.Loss
	if n := req.Native; n != nil && n.Format == llm.FormatGeminiGenerate {
		tr.From, tr.Passthrough = n.Format, true
		body = n.Body
	} else {
		if n != nil {
			tr.From = n.Format
		}
		var err error
		body, losses, err = geminigenerate.EncodeRequest(req)
		if err != nil {
			return nil, nil, err
		}
		if req.Options.ReasoningEffort != "" {
			var applied bool
			body, applied, err = geminigenerate.ApplyEffort(body, req.Model, req.Options.ReasoningEffort)
			if err != nil {
				return nil, nil, err
			}
			if applied {
				losses = dropLoss(losses, "reasoning_effort")
			}
		}
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(body, &inner); err != nil || inner == nil {
		return nil, nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	claude := strings.Contains(strings.ToLower(req.Model), "claude")
	// The model goes in the envelope, and Cloud Code applies no safety settings a client names
	// (today's core removes them too).
	delete(inner, "model")
	delete(inner, "safetySettings")
	if gen, ok := object(inner["generationConfig"]); ok {
		changed := cleanResponseSchema(gen)
		if _, has := gen["maxOutputTokens"]; has && !claude {
			// Today's core never sends a Gemini model an output limit on Cloud Code; the model's
			// own limit applies.
			delete(gen, "maxOutputTokens")
			changed = true
			losses = append(losses, llm.Loss{Kind: llm.LossApproximated, Path: "max_output_tokens", Detail: "Cloud Code's Gemini models write up to their own limit"})
		}
		if changed {
			inner["generationConfig"], _ = json.Marshal(gen)
		}
	}
	if claude {
		// Claude on Cloud Code checks function calls against their declarations only in
		// VALIDATED mode, which today's core always asks for.
		setFunctionCallingMode(inner, "VALIDATED")
	} else {
		functionResponsesAsModel(inner)
		userBoundaries(inner)
	}
	if si, ok := object(inner["systemInstruction"]); ok && !tr.Passthrough && tr.From != llm.FormatOpenAIResponses {
		if _, has := si["role"]; !has {
			// A system instruction translated from Chat or Messages goes as a user turn's
			// content, as today's core sends it (its Responses translation leaves the role out).
			si["role"] = json.RawMessage(`"user"`)
			inner["systemInstruction"], _ = json.Marshal(si)
		}
	}
	if raw, ok := inner["tools"]; ok {
		// Today's core keeps titles where it marks schemas for Claude and Gemini 3 Pro.
		m := strings.ToLower(req.Model)
		keepTitle := claude || strings.Contains(m, "gemini-3-pro") || strings.Contains(m, "gemini-3.1-pro")
		inner["tools"] = cleanTools(raw, claude, keepTitle)
	}
	tr.Losses = losses
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		var paths []string
		for _, l := range losses {
			if l.Kind == llm.LossDropped {
				paths = append(paths, l.Path)
			}
		}
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "Antigravity cannot carry parts of this request: " + strings.Join(paths, ", ")}
	}
	return inner, tr, nil
}

// envelope wraps a GenerateContent body in Cloud Code's request: the model, the project, the
// request type and IDs Antigravity sends, and a session ID for the conversation.
func envelope(inner map[string]json.RawMessage, model, project string) ([]byte, error) {
	env := map[string]any{
		"model":     model,
		"userAgent": "antigravity",
		"project":   project,
	}
	if strings.Contains(model, "image") {
		env["requestType"] = "image_gen"
		env["requestId"] = fmt.Sprintf("image_gen/%d/%s/12", time.Now().UnixMilli(), newUUID())
	} else {
		env["requestType"] = "agent"
		env["requestId"] = "agent-" + newUUID()
		if sid, _ := stringOf(inner["sessionId"]); strings.TrimSpace(sid) == "" {
			inner["sessionId"], _ = json.Marshal(sessionID(inner["contents"]))
		}
	}
	env["request"] = inner
	return json.Marshal(env)
}

// sessionID names a conversation to Cloud Code the way today's core does, so that requests of one
// conversation share it: a hash of the first user turn's first text, or a random ID when there
// is none.
func sessionID(contents json.RawMessage) string {
	var turns []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	_ = json.Unmarshal(contents, &turns)
	for _, t := range turns {
		if t.Role != "user" || len(t.Parts) == 0 || t.Parts[0].Text == "" {
			continue
		}
		sum := sha256.Sum256([]byte(t.Parts[0].Text))
		return "-" + strconv.FormatInt(int64(binary.BigEndian.Uint64(sum[:8])&0x7FFFFFFFFFFFFFFF), 10)
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(9_000_000_000_000_000_000))
	return "-" + n.String()
}

// setFunctionCallingMode sets toolConfig.functionCallingConfig.mode.
func setFunctionCallingMode(inner map[string]json.RawMessage, mode string) {
	tc, _ := object(inner["toolConfig"])
	if tc == nil {
		tc = map[string]json.RawMessage{}
	}
	fc, _ := object(tc["functionCallingConfig"])
	if fc == nil {
		fc = map[string]json.RawMessage{}
	}
	fc["mode"], _ = json.Marshal(mode)
	tc["functionCallingConfig"], _ = json.Marshal(fc)
	inner["toolConfig"], _ = json.Marshal(tc)
}

// functionResponsesAsModel gives each turn that holds only function responses the model role,
// the way Cloud Code's Gemini models take tool results from Antigravity (today's core sends them
// so).
//
// shortcut: today's core also repairs a response's missing name from the call it answers and
// splits turns that mix responses with other parts; add those if a client is seen to need them.
func functionResponsesAsModel(inner map[string]json.RawMessage) {
	var turns []map[string]json.RawMessage
	if json.Unmarshal(inner["contents"], &turns) != nil {
		return
	}
	changed := false
	for _, t := range turns {
		var parts []map[string]json.RawMessage
		if json.Unmarshal(t["parts"], &parts) != nil || len(parts) == 0 {
			continue
		}
		all := true
		for _, p := range parts {
			if _, ok := p["functionResponse"]; !ok {
				all = false
				break
			}
		}
		if role, _ := stringOf(t["role"]); all && role != "model" {
			t["role"] = json.RawMessage(`"model"`)
			changed = true
		}
	}
	if changed {
		inner["contents"], _ = json.Marshal(turns)
	}
}

// userBoundaries makes a Gemini model's conversation start and end with a user turn, as today's
// core sends it: Cloud Code refuses a request that ends with a model turn ("Requests ending with
// a model turn are not supported"). Trailing model turns of thoughts alone are dropped (keeping
// one turn), an empty user turn goes before a leading model turn, and one goes after a trailing
// model turn, except a turn of function responses (which the model answers) without media.
func userBoundaries(inner map[string]json.RawMessage) {
	var turns []map[string]json.RawMessage
	if json.Unmarshal(inner["contents"], &turns) != nil || len(turns) == 0 {
		return
	}
	role := func(t map[string]json.RawMessage) string { r, _ := stringOf(t["role"]); return r }
	parts := func(t map[string]json.RawMessage) []map[string]json.RawMessage {
		var p []map[string]json.RawMessage
		_ = json.Unmarshal(t["parts"], &p)
		return p
	}
	isModel := func(t map[string]json.RawMessage) bool { r := role(t); return r == "model" || r == "assistant" }
	thoughtOnly := func(t map[string]json.RawMessage) bool {
		ps := parts(t)
		if !isModel(t) || len(ps) == 0 {
			return false
		}
		for _, p := range ps {
			_, call := p["functionCall"]
			_, resp := p["functionResponse"]
			if string(p["thought"]) != "true" || call || resp {
				return false
			}
		}
		return true
	}
	answersCalls := func(t map[string]json.RawMessage) bool {
		has := false
		for _, p := range parts(t) {
			fr, ok := object(p["functionResponse"])
			if !ok {
				continue
			}
			has = true
			var nested []map[string]json.RawMessage
			_ = json.Unmarshal(fr["parts"], &nested)
			for _, n := range nested {
				for _, k := range []string{"inlineData", "inline_data", "fileData", "file_data"} {
					if _, media := n[k]; media {
						return false
					}
				}
			}
		}
		return has
	}
	n := len(turns)
	for n > 1 && thoughtOnly(turns[n-1]) {
		n--
	}
	changed := n != len(turns)
	turns = turns[:n]
	empty := map[string]json.RawMessage{"role": json.RawMessage(`"user"`), "parts": json.RawMessage(`[{"text":""}]`)}
	if role(turns[0]) == "model" {
		turns = append([]map[string]json.RawMessage{empty}, turns...)
		changed = true
	}
	if last := turns[len(turns)-1]; isModel(last) && !answersCalls(last) {
		turns = append(turns, empty)
		changed = true
	}
	if changed {
		inner["contents"], _ = json.Marshal(turns)
	}
}

func object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

func stringOf(raw json.RawMessage) (string, bool) {
	var s string
	return s, len(raw) > 0 && json.Unmarshal(raw, &s) == nil
}

func dropLoss(losses []llm.Loss, path string) []llm.Loss {
	out := losses[:0]
	for _, l := range losses {
		if l.Path != path {
			out = append(out, l)
		}
	}
	return out
}

// newUUID returns a random UUID (version 4).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
