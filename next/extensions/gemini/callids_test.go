package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestStripCallIDs(t *testing.T) {
	in := `{"model":"m","note":"<a&b>","contents":[` +
		`{"role":"user","parts":[{"text":"hi"}]},` +
		`{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"f","args":{"id":9007199254740993}},"thoughtSignature":"s"}]},` +
		`{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"f","response":{"id":"keep"}}}]}]}`
	got := stripCallIDs([]byte(in))
	var b struct {
		Contents []struct {
			Parts []struct {
				FunctionCall     map[string]json.RawMessage `json:"functionCall"`
				FunctionResponse map[string]json.RawMessage `json:"functionResponse"`
				ThoughtSignature string                     `json:"thoughtSignature"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(got, &b); err != nil {
		t.Fatalf("not JSON: %v: %s", err, got)
	}
	call, resp := b.Contents[1].Parts[0], b.Contents[2].Parts[0]
	if _, ok := call.FunctionCall["id"]; ok {
		t.Errorf("functionCall.id kept: %s", got)
	}
	if _, ok := resp.FunctionResponse["id"]; ok {
		t.Errorf("functionResponse.id kept: %s", got)
	}
	if string(call.FunctionCall["name"]) != `"f"` || call.ThoughtSignature != "s" {
		t.Errorf("the call lost a field: %s", got)
	}
	for _, keep := range []string{`"args":{"id":9007199254740993}`, `"response":{"id":"keep"}`, `"note":"<a&b>"`} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("body lost %s: %s", keep, got)
		}
	}
}

func TestStripCallIDsLeavesOtherBodies(t *testing.T) {
	for _, body := range []string{``, `not json`, `[]`, `{}`, `{"contents":"x"}`,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
		`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{"id":1}}}]}]}`} {
		if got := stripCallIDs([]byte(body)); string(got) != body {
			t.Errorf("stripCallIDs(%q) = %q", body, got)
		}
	}
}

// A tool call and its result, sent by a client in each format to each kind of account: which IDs
// reach Google.
func TestCallIDsSentToGoogle(t *testing.T) {
	items := []llm.Item{
		{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "weather?"}}},
		{Type: llm.ItemTypeToolCall, CallID: "call_1", Name: "get_weather", Arguments: `{"city":"Oslo"}`},
		{Type: llm.ItemTypeToolResult, CallID: "call_1", Output: "12"},
	}
	for _, tt := range []struct {
		kind, from string
		wantIDs    bool
	}{
		{kindGemini, llm.FormatOpenAIResponses, true},
		{kindGemini, llm.FormatAnthropicMessages, true},
		{kindVertexSA, llm.FormatOpenAIResponses, false},
		{kindVertexKey, llm.FormatOpenAIResponses, false},
		{kindVertexSA, llm.FormatOpenAIChat, false},
		{kindVertexKey, llm.FormatOpenAIChat, false},
		{kindVertexSA, llm.FormatAnthropicMessages, true},
		{kindVertexKey, llm.FormatAnthropicMessages, true},
	} {
		t.Run(tt.kind+"/"+tt.from, func(t *testing.T) {
			tokens = &tokenCache{entries: map[string]*tokenEntry{}}
			srv := geminitest.New(t)
			var body []byte
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				body = req.Body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"message":"stop","status":"INVALID_ARGUMENT"}}`))
			}
			cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
			if tt.kind == kindVertexSA {
				cred = &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}
			}
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Settings: settingsFor(srv, tt.kind), Credential: cred},
				Request: llm.Request{Model: "gemini-x", Native: &llm.Native{Format: tt.from}, Items: items},
			}
			if err := generate(context.Background(), call, nil); err == nil {
				t.Fatal("generate succeeded against a server that refuses")
			}
			if body == nil {
				t.Fatal("no request reached Google")
			}
			has := strings.Contains(string(body), `"id":"call_1"`)
			if has != tt.wantIDs {
				t.Errorf("IDs sent = %v, want %v: %s", has, tt.wantIDs, body)
			}
			if !strings.Contains(string(body), `"name":"get_weather"`) || !strings.Contains(string(body), `"functionResponse"`) {
				t.Errorf("the call or its result is missing: %s", body)
			}
		})
	}
}
