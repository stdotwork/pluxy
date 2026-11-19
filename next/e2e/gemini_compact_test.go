package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
)

// Responses compaction on Gemini accounts (POST /v1/responses/compact and
// /backend-api/codex/responses/compact): a plain Gemini API key account and a Vertex AI service
// account both translate the Responses conversation to non-streamed GenerateContent with the
// Codex CLI's compaction instruction, force text output via toolConfig, strip thought parts, and
// return OpenAI's response.compaction JSON object.
func TestGeminiResponsesCompact(t *testing.T) {
	codexHeaders := map[string]string{
		"User-Agent": "codex_cli_rs/0.150.0 (Linux; x86_64)",
		"Originator": "codex_cli_rs",
		"Version":    "0.150.0",
		"Session_id": "s-1",
	}
	reqBody := `{"model":"gemini-2.5-pro","instructions":"You are a coding agent.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is one file, a.txt."}]}],` +
		`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}],` +
		`"tool_choice":"required","parallel_tool_calls":false,"reasoning":{"effort":"high"},` +
		`"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"}}}}`

	t.Run("gemini api key", func(t *testing.T) {
		srv := geminitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"responseId":"gem_compact_1","modelVersion":"gemini-2.5-pro",`+
				`"candidates":[{"content":{"role":"model","parts":[`+
				`{"text":"Internal thought that must stay hidden.","thought":true},`+
				`{"text":"The user asked for the files; ls showed a.txt."}`+
				`]},"finishReason":"STOP","index":0}],`+
				`"usageMetadata":{"promptTokenCount":144,"cachedContentTokenCount":20,"candidatesTokenCount":76,"thoughtsTokenCount":200}}`)
		}
		base := runCore(t, map[string]any{
			"listen":      "127.0.0.1:0",
			"client_keys": []string{clientKey},
			"extension": map[string]any{
				"manifest": geminiManifest(t),
				"accounts": []any{map[string]any{
					"id":         "key",
					"settings":   map[string]any{"base_url": srv.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": srv.IssueKey()},
				}},
			},
		})
		code, reply := clientPost(t, base, "/v1/responses/compact", reqBody, codexHeaders)
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, reply)
		}

		reqs := srv.Requests()
		if len(reqs) != 1 {
			t.Fatalf("upstream got %d requests, want 1", len(reqs))
		}
		if reqs[0].Stream || reqs[0].Kind != "gemini" || reqs[0].Method != "generateContent" {
			t.Errorf("upstream got kind=%q method=%q stream=%v, want non-streamed gemini generateContent", reqs[0].Kind, reqs[0].Method, reqs[0].Stream)
		}

		var sent struct {
			SystemInstruction json.RawMessage `json:"systemInstruction"`
			Contents          []struct {
				Role  string            `json:"role"`
				Parts []json.RawMessage `json:"parts"`
			} `json:"contents"`
			Tools      json.RawMessage `json:"tools"`
			ToolConfig struct {
				FunctionCallingConfig struct {
					Mode string `json:"mode"`
				} `json:"functionCallingConfig"`
			} `json:"toolConfig"`
			GenerationConfig struct {
				ResponseMimeType string          `json:"responseMimeType"`
				ResponseSchema   json.RawMessage `json:"responseSchema"`
				ThinkingConfig   json.RawMessage `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
			t.Fatalf("upstream body %s: %v", reqs[0].Body, err)
		}
		if !strings.Contains(string(sent.SystemInstruction), "You are a coding agent.") {
			t.Errorf("systemInstruction = %s, want coding agent instructions", sent.SystemInstruction)
		}
		if len(sent.Contents) == 0 || sent.Contents[len(sent.Contents)-1].Role != "user" {
			t.Fatalf("contents = %+v, want trailing user turn with compaction prompt", sent.Contents)
		}
		lastParts, _ := json.Marshal(sent.Contents[len(sent.Contents)-1].Parts)
		if !strings.Contains(string(lastParts), "CONTEXT CHECKPOINT COMPACTION") {
			t.Errorf("last user turn = %s, want Codex compaction prompt", lastParts)
		}
		if sent.ToolConfig.FunctionCallingConfig.Mode != "NONE" || !strings.Contains(string(sent.Tools), `"shell"`) {
			t.Errorf("tools=%s toolConfig=%+v; want shell tool kept with functionCallingConfig NONE", sent.Tools, sent.ToolConfig)
		}
		if sent.GenerationConfig.ResponseMimeType != "" || len(sent.GenerationConfig.ResponseSchema) != 0 {
			t.Errorf("generationConfig = %+v, want structured output cleared", sent.GenerationConfig)
		}
		if len(sent.GenerationConfig.ThinkingConfig) == 0 {
			t.Errorf("generationConfig.thinkingConfig missing from %s", reqs[0].Body)
		}

		var got struct {
			ID     string `json:"id"`
			Object string `json:"object"`
			Model  string `json:"model"`
			Output []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Status  string `json:"status"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				TotalTokens        int `json:"total_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
				OutputTokensDetails struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(reply), &got); err != nil {
			t.Fatalf("reply %s: %v", reply, err)
		}
		if got.Object != "response.compaction" || !strings.HasPrefix(got.ID, "resp_") || got.Model != "gemini-2.5-pro" {
			t.Errorf("reply %s; want response.compaction with resp_ id and model gemini-2.5-pro", reply)
		}
		if len(got.Output) != 1 || got.Output[0].Type != "message" || got.Output[0].Role != "user" ||
			len(got.Output[0].Content) != 1 || got.Output[0].Content[0].Type != "input_text" {
			t.Fatalf("reply output %s; want one user message with one input_text part", reply)
		}
		text := got.Output[0].Content[0].Text
		if !strings.HasPrefix(text, "Another language model started to solve this problem") ||
			!strings.HasSuffix(text, ":\nThe user asked for the files; ls showed a.txt.") {
			t.Errorf("summary text = %q, want Codex summary prefix + newline + summary", text)
		}
		if strings.Contains(text, "Internal thought") {
			t.Errorf("summary text %q leaked thought part", text)
		}
		if got.Usage.InputTokens != 144 || got.Usage.InputTokensDetails.CachedTokens != 20 ||
			got.Usage.OutputTokens != 276 || got.Usage.OutputTokensDetails.ReasoningTokens != 200 ||
			got.Usage.TotalTokens != 420 {
			t.Errorf("usage = %+v, want 144 in (20 cached), 276 out (200 reasoning), 420 total", got.Usage)
		}
	})

	t.Run("codex route on vertex service account", func(t *testing.T) {
		srv := geminitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Vertex summary."}]},"finishReason":"STOP","index":0}],`+
				`"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":12}}`)
		}
		var sa json.RawMessage = srv.ServiceAccount("compact-proj", true)
		base := runCore(t, map[string]any{
			"listen":      "127.0.0.1:0",
			"client_keys": []string{clientKey},
			"extension": map[string]any{
				"manifest": geminiManifest(t),
				"accounts": []any{map[string]any{
					"id": "vertex-sa",
					"settings": map[string]any{
						"base_url": srv.URL,
						"vertex":   map[string]any{"location": "us-central1", "project": "compact-proj"},
					},
					"credential": map[string]any{"kind": "service_account", "data": sa},
				}},
			},
		})
		code, reply := clientPost(t, base, "/backend-api/codex/responses/compact", reqBody, codexHeaders)
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, reply)
		}
		reqs := srv.Requests()
		if len(reqs) != 1 || reqs[0].Kind != "vertex" {
			t.Fatalf("upstream requests = %+v, want 1 vertex request", reqs)
		}
		if strings.Contains(string(reqs[0].Body), `"call_1"`) {
			t.Errorf("Vertex upstream body retained Responses call_id call_1: %s", reqs[0].Body)
		}
		if !strings.Contains(reply, `\nVertex summary.`) {
			t.Errorf("reply = %s, want Vertex summary", reply)
		}
	})

	t.Run("streaming refused", func(t *testing.T) {
		srv := geminitest.New(t)
		base := runCore(t, map[string]any{
			"listen":      "127.0.0.1:0",
			"client_keys": []string{clientKey},
			"extension": map[string]any{
				"manifest": geminiManifest(t),
				"accounts": []any{map[string]any{
					"id":         "key",
					"settings":   map[string]any{"base_url": srv.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": srv.IssueKey()},
				}},
			},
		})
		code, _ := clientPost(t, base, "/v1/responses/compact", `{"model":"gemini-2.5-pro","input":"hi","stream":true}`, codexHeaders)
		if code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", code)
		}
		if got := len(srv.Requests()); got != 0 {
			t.Errorf("upstream got %d requests, want 0", got)
		}
	})
}
