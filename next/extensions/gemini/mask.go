package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func masked(h sdk.Handler) sdk.Handler {
	return func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
		err := h(ctx, call, out)
		if err == nil {
			return nil
		}
		known := knownNames(call.Account)
		var e *llm.Error
		if errors.As(err, &e) {
			return geminigenerate.MaskProject(e, known...)
		}
		m := geminigenerate.MaskProject(&llm.Error{Code: llm.CodeInternal, Message: err.Error()}, known...)
		if m.Message == err.Error() {
			return err
		}
		if ctx.Err() != nil {
			m.Code = llm.CodeCancelled
		}
		return m
	}
}

func knownNames(a protocol.Account) []string {
	var known []string
	if len(a.Settings) > 0 {
		var s struct {
			Vertex *struct {
				Project string `json:"project"`
			} `json:"vertex"`
		}
		if json.Unmarshal(a.Settings, &s) == nil && s.Vertex != nil {
			known = append(known, s.Vertex.Project)
		}
	}
	if c := a.Credential; c != nil && c.Kind == CredentialServiceAccount && len(c.Data) > 0 {
		var k struct {
			ProjectID   string `json:"project_id"`
			ClientEmail string `json:"client_email"`
		}
		if json.Unmarshal(c.Data, &k) == nil {
			known = append(known, k.ProjectID, k.ClientEmail)
		}
	}
	return known
}

func maskReply(data []byte, event string, known []string) []byte {
	return geminigenerate.MaskReply(data, event, known...)
}
