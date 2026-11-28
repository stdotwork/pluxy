package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestEmbeddingsRefusesWiresWithoutEmbeddings(t *testing.T) {
	for _, wire := range []string{wireMessages, wireGemini} {
		t.Run(wire, func(t *testing.T) {
			srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				w.WriteHeader(http.StatusInternalServerError)
			})
			settings := json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"` + wire + `"}`)
			call := sdk.Call{
				Account: protocol.Account{
					ID:         "compat-embeddings",
					Settings:   settings,
					Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
				},
				Request: llm.Request{
					Model: "text-embedding-3-small",
					Native: &llm.Native{
						Format: llm.FormatOpenAIEmbeddings,
						Body:   json.RawMessage(`{"model":"text-embedding-3-small","input":"first"}`),
					},
				},
			}

			err := embeddings(context.Background(), call, nil)
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
				t.Fatalf("err = %+v; want retryable CodeUnsupported on ScopeCredential", err)
			}
			if want := formatNames[wires[wire].format]; !strings.Contains(e.Message, want) {
				t.Errorf("error message = %q, want it to name %q", e.Message, want)
			}
			if got := recorded(); len(got) != 0 {
				t.Errorf("upstream got %d requests, want 0", len(got))
			}
		})
	}
}

func TestEmbeddingsBodyNamesTheRoutedModel(t *testing.T) {
	call := imageCall(llm.FormatOpenAIEmbeddings, "upstream-embed", `{"model":"client-alias","input":["a","b"],"dimensions":256}`)
	body, err := embeddingsBody(call)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "upstream-embed" || got["dimensions"] != float64(256) {
		t.Errorf("body = %s, want model upstream-embed and the other fields kept", body)
	}
	same := `{"model": "upstream-embed", "input": "a"}`
	call = imageCall(llm.FormatOpenAIEmbeddings, "upstream-embed", same)
	if body, err := embeddingsBody(call); err != nil || string(body) != same {
		t.Errorf("body = %s, %v; want the client's bytes unchanged", body, err)
	}
}
