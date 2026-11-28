package main

import (
	"context"
	"encoding/json"
	"io"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func countTokens(ctx context.Context, call sdk.Call, out *sdk.Stream) (err error) {
	ctx = withAccountID(ctx, call.Account.ID)
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	body, tr, err := buildBodyOn(call.Request, s)
	if err != nil {
		return err
	}
	body = cleanToolSchemas(body)
	body = fixLeadingTurn(body)
	switch {
	case !tr.Passthrough:
		body, err = countBody(body, call.Request.Model, s.Vertex != nil)
	case s.Vertex != nil:
		body, err = unwrapCount(body)
	}
	if err != nil {
		return err
	}
	t, err := resolveMethod(ctx, s, call.Account.Credential, call.Request.Model, ":countTokens")
	if err != nil {
		return err
	}
	if t.token != "" {
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

func unwrapCount(body []byte) ([]byte, error) {
	var w struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &w) != nil || len(w.GenerateContentRequest) == 0 || string(w.GenerateContentRequest) == "null" {
		return body, nil
	}
	return countBody(w.GenerateContentRequest, "", true)
}
