package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestMessagesMaxTokensLeftToModel(t *testing.T) {
	limit := 32000
	req := llm.Request{Model: "gemini-2.5-pro", Native: &llm.Native{Format: llm.FormatAnthropicMessages},
		Items:   []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Options: llm.Options{MaxOutputTokens: &limit}}
	body, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "maxOutputTokens") {
		t.Errorf("body %s carries a Messages client's max_tokens", body)
	}
	if len(tr.Losses) == 0 || tr.Losses[0].Path != "max_tokens" {
		t.Errorf("losses = %+v, want max_tokens reported", tr.Losses)
	}
	req.Native.Format = llm.FormatOpenAIChat
	body, _, _ = buildBody(req)
	if !strings.Contains(string(body), `"maxOutputTokens":32000`) {
		t.Errorf("a Chat client's max_tokens is missing from %s", body)
	}
}

func TestUpstreamErrors(t *testing.T) {
	srv := geminitest.New(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) { geminitest.RateLimited(w, 7) }
	key := srv.IssueKey()
	resp := post(t, srv.URL+"/v1beta/models/m:generateContent", key)
	if e := upstreamError(resp, "m"); e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || !e.Retryable || e.RetryAfterMS != 7000 || e.ProviderCode != "RESOURCE_EXHAUSTED" {
		t.Errorf("429 with RetryInfo 7s = %+v, want a retryable rate limit on the model", e)
	}
	// A used-up quota (ErrorInfo reason QUOTA_EXHAUSTED) is a quota error, not a rate limit.
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED","details":[`+
			`{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},`+
			`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3600s"}]}}`)
	}
	resp = post(t, srv.URL+"/v1beta/models/m:generateContent", key)
	if e := upstreamError(resp, "m"); e.Code != llm.CodeQuota || e.Scope != llm.ScopeModel || !e.Retryable || e.RetryAfterMS != 3_600_000 {
		t.Errorf("429 QUOTA_EXHAUSTED = %+v, want a retryable quota error on the model with the retry delay", e)
	}
	// The same 429 on a call for no model (login or the model list) is the account's.
	resp = post(t, srv.URL+"/v1beta/models/m:generateContent", key)
	if e := upstreamError(resp, ""); e.Code != llm.CodeQuota || e.Scope != llm.ScopeCredential {
		t.Errorf("429 for no model = %+v, want it on the credential", e)
	}
	resp = post(t, srv.URL+"/v1beta/models/m:generateContent", "not-a-key")
	if e := upstreamError(resp, "m"); e.Code != llm.CodeAuth || !e.Retryable || e.Scope != llm.ScopeCredential || e.Status != http.StatusBadRequest {
		t.Errorf("400 API_KEY_INVALID = %+v, want a retryable auth error with Google's status", e)
	}
}

func post(t *testing.T, url, key string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	req.Header.Set("X-Goog-Api-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestVertexURLs(t *testing.T) {
	key := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}
	for _, c := range []struct{ base, want string }{
		{"", "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
		{"https://example.test", "https://example.test/v1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
		{"https://example.test/v1beta1", "https://example.test/v1beta1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
		{"https://example.test/v1/publishers/google/models", "https://example.test/v1/publishers/google/models/gemini-x:streamGenerateContent?alt=sse"},
	} {
		tg, err := (&vertexSettings{}).resolve(context.Background(), c.base, key, "gemini-x", method(true))
		if err != nil || tg.url != c.want {
			t.Errorf("API key, base %q: %q (%v), want %q", c.base, tg.url, err, c.want)
		}
	}
	for loc, host := range map[string]string{"": "https://aiplatform.googleapis.com", "global": "https://aiplatform.googleapis.com",
		"us": "https://aiplatform.us.rep.googleapis.com", "europe-west4": "https://europe-west4-aiplatform.googleapis.com"} {
		if got := vertexHost(loc); got != host {
			t.Errorf("vertexHost(%q) = %q, want %q", loc, got, host)
		}
	}
	if err := (&vertexSettings{Location: "us-central1/../x"}).check(); err == nil {
		t.Error("a location with a path in it was accepted")
	}
}

// TestServiceAccount sends requests from a service account: they go to its project's path with
// a bearer token, and concurrent requests share one token from the token endpoint.
func TestServiceAccount(t *testing.T) {
	srv := geminitest.New(t)
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	cred := &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}
	s, err := readSettings(settingsFor(srv, kindVertexSA))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tg, err := resolve(context.Background(), s, cred, "gemini-x", false)
			if err != nil {
				t.Error(err)
				return
			}
			req, _ := http.NewRequest(http.MethodPost, tg.url, strings.NewReader(`{"contents":[]}`))
			tg.authorize(req.Header)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	if n := srv.Grants(); n != 1 {
		t.Errorf("%d tokens fetched for 10 requests, want 1", n)
	}
	reqs := srv.Requests()
	if len(reqs) != 10 || reqs[0].Kind != "vertex" || reqs[0].Project != "conformance-project" || reqs[0].Location != "us-central1" {
		t.Fatalf("requests = %+v, want 10 to the settings' project in us-central1", reqs)
	}

	_, err = resolve(context.Background(), s, &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", false)}, "gemini-x", false)
	var e *llm.Error
	if !asError(err, &e) || e.Code != llm.CodeAuth || !e.Retryable || e.Scope != llm.ScopeCredential {
		t.Errorf("unknown service account: %v, want a retryable auth error", err)
	}
	srv.TokenDown = true
	_, err = resolve(context.Background(), s, &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-2", true)}, "gemini-x", false)
	if !asError(err, &e) || e.Code != llm.CodeUnavailable || !e.Retryable {
		t.Errorf("token endpoint down: %v, want a retryable unavailable error", err)
	}
}

func asError(err error, e **llm.Error) bool {
	le, ok := err.(*llm.Error)
	*e = le
	return ok
}

func TestThinkingLevelsAndSummary(t *testing.T) {
	userItem := []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}}
	hasLoss := func(losses []llm.Loss, kind, path string) bool {
		return slices.ContainsFunc(losses, func(l llm.Loss) bool { return l.Kind == kind && l.Path == path })
	}

	t.Run("xhigh kept on neon and approximated on carbon", func(t *testing.T) {
		neonReq := llm.Request{Model: "gemini-4-neon", Items: userItem, Options: llm.Options{ReasoningEffort: "xhigh"},
			Native: &llm.Native{Format: llm.FormatOpenAIChat}}
		body, tr, err := buildBody(neonReq)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thinkingLevel":"xhigh"`) {
			t.Errorf("neon body = %s, want thinkingLevel xhigh", body)
		}
		if len(tr.Losses) != 0 {
			t.Errorf("neon losses = %+v, want none", tr.Losses)
		}

		carbonReq := neonReq
		carbonReq.Model = "gemini-4-carbon"
		body, tr, err = buildBody(carbonReq)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thinkingLevel":"high"`) {
			t.Errorf("carbon body = %s, want thinkingLevel high", body)
		}
		if !hasLoss(tr.Losses, llm.LossApproximated, "generationConfig.thinkingConfig.thinkingLevel") {
			t.Errorf("carbon losses = %+v, want approximated thinkingLevel", tr.Losses)
		}

		passReq := llm.Request{Model: "gemini-4-carbon", Items: userItem, Options: llm.Options{ThinkingSuffix: "xhigh"},
			Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}}
		body, tr, err = buildBody(passReq)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thinkingLevel":"high"`) {
			t.Errorf("passthrough suffix body = %s, want thinkingLevel high", body)
		}
		if !hasLoss(tr.Losses, llm.LossApproximated, "generationConfig.thinkingConfig.thinkingLevel") {
			t.Errorf("passthrough suffix losses = %+v, want approximated thinkingLevel", tr.Losses)
		}
	})

	t.Run("Claude thinking.display", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			model       string
			effort      string
			thinkingRaw string
			wantTC      string
			wantDropped bool
		}{
			{"omitted without effort", "gemini-3-flash-preview", "", `{"display":"omitted"}`, `{"includeThoughts":false}`, false},
			{"summarized without effort", "gemini-2.5-pro", "", `{"display":"summarized"}`, `{"includeThoughts":true}`, false},
			{"omitted overrides effort includeThoughts", "gemini-3-flash-preview", "high", `{"display":"omitted"}`, `{"includeThoughts":false,"thinkingLevel":"high"}`, false},
			{"summarized with budget_tokens keeps thinking drop", "gemini-2.5-pro", "low", `{"budget_tokens":1024,"display":"summarized"}`, `{"includeThoughts":true,"thinkingBudget":1024}`, true},
			{"summarized ignored when budget 0 disables thinking", "gemini-2.5-flash", "none", `{"display":"summarized"}`, `{"thinkingBudget":0}`, true},
			{"omitted enforced when budget 0 disables thinking", "gemini-2.5-flash", "none", `{"display":"omitted"}`, `{"includeThoughts":false,"thinkingBudget":0}`, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := llm.Request{
					Model:   tc.model,
					Items:   userItem,
					Options: llm.Options{ReasoningEffort: tc.effort},
					Native:  &llm.Native{Format: llm.FormatAnthropicMessages},
					Origin: &llm.Origin{Format: llm.FormatAnthropicMessages,
						Raw: json.RawMessage(`{"thinking":` + tc.thinkingRaw + `}`)},
				}
				body, tr, err := buildBody(req)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					GenerationConfig struct {
						ThinkingConfig json.RawMessage `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if string(got.GenerationConfig.ThinkingConfig) != tc.wantTC {
					t.Errorf("thinkingConfig = %s, want %s", got.GenerationConfig.ThinkingConfig, tc.wantTC)
				}
				if hasLoss(tr.Losses, llm.LossDropped, "thinking") != tc.wantDropped {
					t.Errorf("losses = %+v, want dropped thinking=%v", tr.Losses, tc.wantDropped)
				}
			})
		}
	})

	t.Run("Responses reasoning.summary", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			model        string
			effort       string
			reasoningRaw string
			wantTC       string
			wantDropped  bool
			wantApprox   bool
		}{
			{"auto with effort", "gemini-3-flash-preview", "medium", `{"summary":"auto"}`, `{"includeThoughts":true,"thinkingLevel":"medium"}`, false, false},
			{"detailed approximated", "gemini-3-flash-preview", "high", `{"summary":"detailed"}`, `{"includeThoughts":true,"thinkingLevel":"high"}`, false, true},
			{"none without effort", "gemini-3-flash-preview", "", `{"summary":"none"}`, `{"includeThoughts":false}`, false, false},
			{"null without effort", "gemini-3-flash-preview", "", `{"summary":null}`, `{"includeThoughts":false}`, false, false},
			{"generate_summary omitted", "gemini-2.5-pro", "low", `{"generate_summary":"none"}`, `{"includeThoughts":false,"thinkingBudget":1024}`, false, false},
			{"non-thinking model untouched", "gemini-corpus", "", `{"summary":"auto"}`, ``, true, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := llm.Request{
					Model:   tc.model,
					Items:   userItem,
					Options: llm.Options{ReasoningEffort: tc.effort},
					Native:  &llm.Native{Format: llm.FormatOpenAIResponses},
					Origin: &llm.Origin{Format: llm.FormatOpenAIResponses,
						Raw: json.RawMessage(`{"reasoning":` + tc.reasoningRaw + `}`)},
				}
				body, tr, err := buildBody(req)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					GenerationConfig struct {
						ThinkingConfig json.RawMessage `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if string(got.GenerationConfig.ThinkingConfig) != tc.wantTC {
					t.Errorf("thinkingConfig = %s, want %s", got.GenerationConfig.ThinkingConfig, tc.wantTC)
				}
				if hasLoss(tr.Losses, llm.LossDropped, "reasoning") != tc.wantDropped {
					t.Errorf("losses = %+v, want dropped reasoning=%v", tr.Losses, tc.wantDropped)
				}
				if tc.wantApprox && !hasLoss(tr.Losses, llm.LossApproximated, "reasoning.summary") {
					t.Errorf("losses = %+v, want approximated reasoning.summary", tr.Losses)
				}
			})
		}
	})
}
