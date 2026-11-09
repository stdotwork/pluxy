package main

import (
	"context"
	"encoding/json"
	"io"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// countTokens counts the request's input tokens with the model's countTokens method. A Gemini
// client's countTokens body is sent as it came; a request in another format is rebuilt as
// GenerateContent and counted whole: for the Gemini API under generateContentRequest, for
// Vertex AI (which takes no generateContentRequest) as its contents, system instruction and
// tools.
func countTokens(ctx context.Context, call sdk.Call, out *sdk.Stream) (err error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	body, tr, err := buildBody(call.Request)
	if err != nil {
		return err
	}
	if !tr.Passthrough {
		if body, err = countBody(body, call.Request.Model, s.Vertex != nil); err != nil {
			return err
		}
	}
	t, err := resolveMethod(ctx, s, call.Account.Credential, call.Request.Model, ":countTokens")
	if err != nil {
		return err
	}
	if t.token != "" {
		// As in generate: the fetched access token is redacted here.
		redact := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: t.token})
		defer func() {
			if e, ok := err.(*llm.Error); ok {
				err = redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
			}
		}()
	}
	resp, err := postBody(ctx, t, body, false, call.Request.Model)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxFrame))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUpstream, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	var r struct {
		TotalTokens *int64 `json:"totalTokens"`
	}
	if json.Unmarshal(raw, &r) != nil || r.TotalTokens == nil {
		return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "the upstream's countTokens reply has no totalTokens", Raw: llm.RawJSON(raw)}
	}
	if err := out.Start(llm.ResponseInfo{Model: call.Request.Model, Format: llm.FormatGeminiGenerate}); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, &llm.Usage{InputTokens: r.TotalTokens, Raw: llm.RawJSON(raw)})
}

// countBody returns the countTokens body for a GenerateContent body built for model.
func countBody(body []byte, model string, vertex bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	if vertex {
		out := map[string]json.RawMessage{}
		for _, k := range []string{"contents", "systemInstruction", "tools"} {
			if v, ok := m[k]; ok {
				out[k] = v
			}
		}
		return json.Marshal(out)
	}
	m["model"], _ = json.Marshal("models/" + model)
	return json.Marshal(map[string]any{"generateContentRequest": m})
}
