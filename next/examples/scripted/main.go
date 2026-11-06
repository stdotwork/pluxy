// Command scripted is the example provider extension from the protocol tutorial. It calls no
// upstream: it answers with whatever its account settings say, which also makes it a handy
// stand-in provider for tests.
//
// Account settings:
//
//	{"reply": ["Hello", " there."], "usage": {"input_tokens": 3, "output_tokens": 2}}
//	{"error": {"code": "rate_limit", "message": "slow down", "retryable": true}}
//	{"reply": ["Wait"], "hang": true}
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

type settings struct {
	Reply []string   `json:"reply"`
	Usage *llm.Usage `json:"usage"`
	Error *llm.Error `json:"error"`
	// Hang keeps the reply open after the text until the call is cancelled.
	Hang bool `json:"hang"`
}

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:       "example/scripted",
		Version:  "0.1.0",
		Generate: generate,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "scripted:", err)
		os.Exit(1)
	}
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	var s settings
	if err := json.Unmarshal(call.Account.Settings, &s); err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad account settings", Scope: llm.ScopeCredential}
	}
	if s.Error != nil {
		return s.Error // sent to core as a structured error
	}
	if err := out.Start(llm.ResponseInfo{ID: "scripted-1", Model: call.Request.Model}); err != nil {
		return err
	}
	item, err := out.StartItem(llm.ItemStart{Kind: llm.ItemMessage})
	if err != nil {
		return err
	}
	for _, piece := range s.Reply {
		if err := out.Text(item, piece); err != nil {
			return err
		}
	}
	if s.Hang {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := out.EndItem(item); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop, RawReason: "stop"}, s.Usage)
}
