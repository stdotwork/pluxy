package main

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{
		Manifest: manifest,
		Prepare: func(t *testing.T, s conformance.Scenario) conformance.Setup {
			return conformance.Setup{Account: accountFor(s)}
		},
	})
}

// accountFor returns account settings that make this extension act out a scenario.
func accountFor(s conformance.Scenario) protocol.Account {
	var set settings
	switch s {
	case conformance.ScenarioText:
		set.Reply = []string{conformance.ReplyText[:6], conformance.ReplyText[6:]}
		set.Usage = &llm.Usage{InputTokens: llm.Int64(conformance.InputTokens), OutputTokens: llm.Int64(conformance.OutputTokens)}
	case conformance.ScenarioHang:
		set.Reply, set.Hang = []string{"Hold on"}, true
	case conformance.ScenarioRateLimited:
		set.Error = &llm.Error{Code: llm.CodeRateLimit, Message: "slow down", Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 20000}
	case conformance.ScenarioAuthFailed:
		set.Error = &llm.Error{Code: llm.CodeAuth, Message: "bad key", Retryable: true, Scope: llm.ScopeCredential}
	}
	b, _ := json.Marshal(set)
	return protocol.Account{ID: "scripted", Settings: b}
}
