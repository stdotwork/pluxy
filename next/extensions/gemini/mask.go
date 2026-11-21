package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A client holding a pluxy key must not learn which Google project or account the owner's upstream
// account is. Google names it in its errors, in the message and in the details; the details stay
// (clients read RetryInfo and QuotaFailure) and what names the project is masked
// (geminigenerate.MaskProject).
//
// masked wraps the handlers whose errors reach clients: Generate, CountTokens and Endpoint.
// Login, Check, Quota and Models are the owner's, who reads them to find which project needs an
// API enabled, and are not wrapped. An error is masked after the handler classified it, so its
// code, scope and delays are the ones the handler chose.
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
		// Any other error becomes an internal error with its text, which can hold a request URL
		// that names the project.
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

// knownNames are what the account itself says is its Google project or identity: the project of
// its Vertex settings, and its service account key's project and address.
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

// maskReply masks a reply or an event an Interactions client gets as the upstream sent it, when
// it holds a Google error: an object with an "error" member, or the data of an event named error.
func maskReply(data []byte, event string, known []string) []byte {
	if event != "error" && !bytes.Contains(data, []byte(`"error"`)) {
		return data
	}
	if event != "error" {
		var m map[string]json.RawMessage
		if json.Unmarshal(data, &m) != nil || len(bytes.TrimSpace(m["error"])) == 0 || bytes.TrimSpace(m["error"])[0] != '{' {
			return data
		}
	}
	return geminigenerate.MaskBody(data, known...)
}
