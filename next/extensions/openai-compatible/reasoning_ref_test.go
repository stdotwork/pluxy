package main

// This file holds applyReasoning as it was before it read the body's effort with a scan, and
// compares the two over the bodies the tests in this package build and a generated set.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// applyReasoningRef is applyReasoning as it was: it decoded every body into a map to read the
// effort and report a clamped one as a loss.
func applyReasoningRef(body []byte, req llm.Request, target string, levels []string) ([]byte, []llm.Loss, error) {
	suffix := req.Options.ThinkingSuffix
	switch target {
	case llm.FormatAnthropicMessages:
		if suffix == "" {
			return body, nil, nil
		}
		out, _, err := anthropicmessages.ApplyEffort(body, req.Model, suffix)
		return out, nil, err
	case llm.FormatGeminiGenerate:
		if suffix == "" {
			return body, nil, nil
		}
		out, _, err := geminigenerate.ApplyEffort(body, req.Model, suffix)
		return out, nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, nil, nil
	}
	// Where the effort lives: reasoning_effort for Chat, reasoning.effort for Responses.
	path := "reasoning.effort"
	if target == llm.FormatOpenAIChat {
		path = "reasoning_effort"
	}
	get := func() string {
		var e string
		if target == llm.FormatOpenAIChat {
			_ = json.Unmarshal(m["reasoning_effort"], &e)
			return e
		}
		var r struct {
			Effort string `json:"effort"`
		}
		_ = json.Unmarshal(m["reasoning"], &r)
		return r.Effort
	}
	set := func(e string) {
		v, _ := json.Marshal(e)
		if target == llm.FormatOpenAIChat {
			m["reasoning_effort"] = v
			return
		}
		r := map[string]json.RawMessage{}
		_ = json.Unmarshal(m["reasoning"], &r)
		r["effort"] = v
		m["reasoning"], _ = json.Marshal(r)
	}
	effort := get()
	if suffix != "" {
		effort = llm.ThinkingEffort(suffix)
	}
	clamped := clampEffort(effort, levels)
	var losses []llm.Loss
	if effort != "" && clamped != effort {
		losses = []llm.Loss{{Kind: llm.LossApproximated, Path: path,
			Detail: fmt.Sprintf("reasoning effort %q is not one the account takes; %q was sent", effort, clamped)}}
	}
	if clamped == get() {
		return body, losses, nil
	}
	set(clamped)
	out, err := json.Marshal(m)
	return out, losses, err
}

// TestApplyReasoningMatchesReference compares applyReasoning with the reference copy over every
// body, target, suffix and level set it is given, byte for byte.
func TestApplyReasoningMatchesReference(t *testing.T) {
	targets := []string{llm.FormatOpenAIChat, llm.FormatOpenAIResponses, llm.FormatAnthropicMessages, llm.FormatGeminiGenerate}
	suffixes := []string{"", "none", "auto", "minimal", "low", "medium", "high", "xhigh", "max", "turbo",
		"0", "1", "512", "1024", "8192", "16384", "32000", "-1", "65536"}
	levels := [][]string{nil, {}, {"none", "low", "high"}, {"low", "high"}, {"minimal", "low", "medium", "high", "xhigh"}, {"bogus"}}
	bodies := reasoningBodies(t)
	n := 0
	for name, body := range bodies {
		for _, target := range targets {
			for _, suffix := range suffixes {
				for _, lv := range levels {
					n++
					req := llm.Request{Model: "m", Options: llm.Options{ThinkingSuffix: suffix}}
					got := applyOutcome(func() ([]byte, error) { b, _, err := applyReasoning([]byte(body), req, target, lv); return b, err })
					want := applyOutcome(func() ([]byte, error) { b, _, err := applyReasoningRef([]byte(body), req, target, lv); return b, err })
					if got.panic != want.panic {
						t.Fatalf("%s/%s/%q/%q: panic = %v, reference %v", name, target, suffix, lv, got.panic, want.panic)
					}
					if (got.err == nil) != (want.err == nil) {
						t.Fatalf("%s/%s/%q/%q: error = %v, reference %v", name, target, suffix, lv, got.err, want.err)
					}
					if got.err != nil && got.err.Error() != want.err.Error() {
						t.Fatalf("%s/%s/%q/%q: error = %v, reference %v", name, target, suffix, lv, got.err, want.err)
					}
					if string(got.body) != string(want.body) {
						t.Fatalf("%s/%s/%q/%q: body =\n %s\nreference\n %s", name, target, suffix, lv, got.body, want.body)
					}
				}
			}
		}
	}
	t.Logf("%d bodies, %d cases compared with the reference", len(bodies), n)
}

// outcome is what applyReasoning did, a panic included: a body holding a null reasoning (or no
// object where one is read) makes today's decode-and-set path panic, and that answer stays too.
type outcome struct {
	body  []byte
	err   error
	panic any
}

func applyOutcome(f func() ([]byte, error)) (o outcome) {
	defer func() { o.panic = recover() }()
	o.body, o.err = f()
	return o
}

// reasoningBodies returns the bodies the bodies in this package's tests hold and the edges a
// reading of the effort has to get right.
func reasoningBodies(t *testing.T) map[string]string {
	t.Helper()
	bodies := map[string]string{
		// The bodies reasoning_test.go and the extension's own tests build.
		"chat-effort":         `{"model":"m","reasoning_effort":"high"}`,
		"chat-no-effort":      `{"model":"m"}`,
		"chat-clamp":          `{"model":"m","reasoning_effort":"xhigh"}`,
		"responses-reasoning": `{"model":"m","reasoning":{"summary":"auto"}}`,
		"translated": `{"model":"m","messages":[{"role":"system","content":"Be brief."},` +
			`{"role":"user","content":[{"type":"text","text":"Weather in Oslo and Bergen?"},` +
			`{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"low"}}]},` +
			`{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call_1","type":"function",` +
			`"function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]},` +
			`{"role":"tool","content":"4C rain","tool_call_id":"call_1"}],` +
			`"stream":true,"max_tokens":50,"temperature":0.5,"stop":["END"],` +
			`"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`,
		// The effort where it lives, and nothing else.
		"chat-effort-empty":    `{"reasoning_effort":""}`,
		"chat-effort-null":     `{"reasoning_effort":null}`,
		"chat-effort-number":   `{"reasoning_effort":5}`,
		"chat-effort-true":     `{"reasoning_effort":true}`,
		"chat-effort-object":   `{"reasoning_effort":{"effort":"high"}}`,
		"chat-effort-array":    `{"reasoning_effort":["high"]}`,
		"chat-effort-escaped":  `{"reasoning_effor\u0074":"low"}`,
		"chat-effort-case":     `{"Reasoning_Effort":"low"}`,
		"chat-effort-twice":    `{"reasoning_effort":"xhigh","reasoning_effort":null}`,
		"chat-effort-twice-2":  `{"reasoning_effort":"high","reasoning_effort":"max"}`,
		"chat-effort-invalid":  `{"reasoning_effort":"\ud800"}`,
		"chat-effort-bad-byte": "{\"reasoning_effort\":\"a\xffb\"}",
		"chat-key-bad-byte":    "{\"reasoning_effort\xff\":\"low\"}",
		"chat-key-escaped-sum": `{"\u0072easoning_effort":"medium"}`,
		"chat-other-keys":      `{"model":"m","reasoning_effort":"low","x":1,"y":[1,2,{"z":null}]}`,
		// The Responses shape, at every depth.
		"responses-effort":        `{"model":"m","reasoning":{"effort":"high"}}`,
		"responses-effort-null":   `{"model":"m","reasoning":{"effort":null}}`,
		"responses-effort-case":   `{"model":"m","reasoning":{"EFFORT":"low"}}`,
		"responses-effort-mixed":  `{"model":"m","reasoning":{"Effort":"high"}}`,
		"responses-effort-num":    `{"model":"m","reasoning":{"effort":3}}`,
		"responses-effort-twice":  `{"model":"m","reasoning":{"effort":"high","effort":null}}`,
		"responses-effort-twice2": `{"model":"m","reasoning":{"effort":null,"effort":"max"}}`,
		"responses-effort-worse":  `{"model":"m","reasoning":{"effort":{"a":1},"effort":"low"}}`,
		"responses-escaped-name":  `{"model":"m","reasoning":{"effor\u0074":"high"}}`,
		"responses-string":        `{"model":"m","reasoning":"high"}`,
		"responses-number":        `{"model":"m","reasoning":5}`,
		"responses-null":          `{"model":"m","reasoning":null}`,
		"responses-array":         `{"model":"m","reasoning":["high"]}`,
		"responses-twice":         `{"model":"m","reasoning":{"effort":"low"},"reasoning":{"effort":"max"}}`,
		"responses-summary":       `{"model":"m","reasoning":{"effort":"high","summary":"auto"}}`,
		// What is not one JSON object, or not JSON.
		"empty-object": `{}`,
		"null":         `null`,
		"array":        `[]`,
		"string":       `"str"`,
		"number":       `5`,
		"true":         `true`,
		"invalid":      `{"model":`,
		"trailing":     `{"model":"m"} x`,
		"two-values":   `{"model":"m"}{"model":"n"}`,
		"empty":        ``,
		"deep":         strings.Repeat(`{"a":`, 60) + `1` + strings.Repeat(`}`, 60),
		// Whitespace and a body near the size an upstream sees.
		"spaced": "{ \"model\" : \"m\" ,\n  \"reasoning\" : { \"effort\" : \"high\" } }",
		"big":    `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("lorem ipsum ", 8000) + `"}],"reasoning":{"effort":"high"}}`,
	}
	// A body whose effort only appears where the decoder must not look for it.
	for _, b := range []string{
		`{"model":"m","messages":[{"reasoning_effort":"low"}]}`,
		`{"reasoning":{"effort":"low"},"model":"m"}`,
	} {
		bodies["nested-only/"+b] = b
	}
	return bodies
}
