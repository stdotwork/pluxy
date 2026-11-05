package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestPayloadBarrierCodexImageFilter(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-luna"} {
				t.Run(transport+"/"+model+map[bool]string{false: "/execute", true: "/stream"}[stream], func(t *testing.T) {
					captured := make(chan []byte, 1)
					completed := []byte(`{"type":"response.completed","response":{"id":"resp_barrier","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if transport == "websocket" {
							upgrader := websocket.Upgrader{}
							conn, errUpgrade := upgrader.Upgrade(w, r, nil)
							if errUpgrade != nil {
								t.Error(errUpgrade)
								return
							}
							defer func() {
								if errClose := conn.Close(); errClose != nil {
									t.Error(errClose)
								}
							}()
							_, body, errRead := conn.ReadMessage()
							if errRead != nil {
								t.Error(errRead)
								return
							}
							captured <- body
							if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
								t.Error(errWrite)
							}
							return
						}
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Error(errRead)
							return
						}
						captured <- body
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write(append(append([]byte("data: "), completed...), '\n', '\n'))
					}))
					defer server.Close()
					cfg := &config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gpt-5.6-sol"}}, Params: []string{`tools.#(type=="image_generation")#`, "instructions", "prompt_cache_key"}}}}}
					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if transport == "websocket" {
						executor = NewCodexWebsocketsExecutor(cfg)
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "plan_type": "pro"}}
					req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hello","prompt_cache_key":"injected-cache"}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
					if stream {
						result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
					body := <-captured
					image := ojson.GetBytes(body, `tools.#(type=="image_generation")`).Exists()
					if image != (model == "gpt-5.6-luna") {
						t.Fatalf("image tool presence = %v: %s", image, body)
					}
					if model == "gpt-5.6-sol" && (ojson.GetBytes(body, "instructions").Exists() || ojson.GetBytes(body, "prompt_cache_key").Exists()) {
						t.Fatalf("late injection undid filter: %s", body)
					}
				})
			}
		}
	}
}

func TestPayloadBarrierAntigravityRebuiltAttempts(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: map[string]any{"generationConfig.maxOutputTokens": 123, "toolConfig.functionCallingConfig.mode": "CUSTOM"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: []string{"sessionId", "contents.0"}}},
	}}
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview"}
	body := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"user","parts":[{"text":"second"}]}]}}`)
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", req.Model, "antigravity", "request", body, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}))
	executor := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-test"}}
	for attempt := 0; attempt < 2; attempt++ {
		request, errBuild := executor.buildRequest(ctx, auth, "token", req.Model, body, attempt == 1, "", "https://example.invalid", "injected-session")
		if errBuild != nil {
			t.Fatal(errBuild)
		}
		wire, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errClose := request.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if ojson.GetBytes(wire, "request.generationConfig.maxOutputTokens").Int() != 123 {
			t.Fatalf("built-in cleanup undid override: %s", wire)
		}
		if ojson.GetBytes(wire, "request.sessionId").Exists() {
			t.Fatalf("session reinjected: %s", wire)
		}
		if got := ojson.GetBytes(wire, "request.contents.#").Int(); got != 1 {
			t.Fatalf("filter applied more than once: %s", wire)
		}
	}
}

func TestPayloadBarrierAIStudio(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"generationConfig.maxOutputTokens": 900000, "generationConfig.thinkingConfig.thinkingLevel": "low"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"contents"}}},
	}}
	executor := NewAIStudioExecutor(cfg, "aistudio", nil)
	for _, stream := range []bool{false, true} {
		body, _, errTranslate := executor.translateRequest(context.Background(), cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"contents":[{"role":"model","parts":[{"text":"hi"}]}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}, stream)
		if errTranslate != nil {
			t.Fatal(errTranslate)
		}
		if ojson.GetBytes(body, "contents").Exists() || ojson.GetBytes(body, "generationConfig.maxOutputTokens").Int() != 900000 || ojson.GetBytes(body, "generationConfig.thinkingConfig.thinkingLevel").String() != "low" {
			t.Fatalf("late normalization undid rules: %s", body)
		}
	}
}

func TestPayloadBarrierXAIWebsocketRetry(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"store": false, "instructions": "configured"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"input.0", "previous_response_id"}}},
	}}
	req := cliproxyexecutor.Request{Model: "grok-4.3"}
	body := []byte(`{"previous_response_id":"previous","input":[{"content":"first"},{"content":"second"}]}`)
	finalize := helps.NewPayloadFinalizer(cfg, "xai", req.Model, "codex", "", body, req, cliproxyexecutor.Options{})
	first := buildXAIWebsocketRequestBody(body, finalize)
	retry := buildXAIWebsocketRequestBody(body, finalize)
	if !bytes.Equal(first, retry) {
		t.Fatalf("retry changed configured body: %s != %s", first, retry)
	}
	if ojson.GetBytes(first, "store").Bool() || ojson.GetBytes(first, "instructions").String() != "configured" || ojson.GetBytes(first, "previous_response_id").Exists() || ojson.GetBytes(first, "input.#").Int() != 1 {
		t.Fatalf("invalid final payload: %s", first)
	}
	if ojson.GetBytes(first, "type").String() != "response.create" {
		t.Fatalf("missing frame type: %s", first)
	}
}

func TestPayloadBarrierClaudeDoesNotReplayFiltersOrReinjectIdentity(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"system", "metadata", "diagnostics", "context_management", "messages.0"}}},
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"max_tokens": 1}}},
	}}
	for _, stream := range []bool{false, true} {
		body := executeClaudeContextManagementRequest(t, cfg, []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}]}`), stream)
		for _, path := range []string{"system", "metadata", "diagnostics", "context_management"} {
			if ojson.GetBytes(body, path).Exists() {
				t.Fatalf("%s was reinjected: %s", path, body)
			}
		}
		if ojson.GetBytes(body, "messages.#").Int() != 2 || ojson.GetBytes(body, "max_tokens").Int() != 1 {
			t.Fatalf("built-in processing changed final rule semantics: %s", body)
		}
	}
}

type captureEffectiveServiceTierPlugin struct {
	authID  string
	records chan usage.Record
}

func (p *captureEffectiveServiceTierPlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || record.AuthID != p.authID {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func TestPayloadBarrierEffectiveServiceTierUsageAfterOverride(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{
					{Name: "gpt-*", Protocol: "codex"},
					{Name: "gpt-*", Protocol: "openai"},
					{Name: "grok-*", Protocol: "openai"},
					{Name: "*", Protocol: "devin"},
				},
				Params: map[string]any{"service_tier": "priority"},
			}},
		},
	}

	t.Run("codex oauth chat completions override", func(t *testing.T) {
		var captured capturedCodexRequest
		server := newCodexRoutingHintServer(t, &captured)
		defer server.Close()

		for _, stream := range []bool{false, true} {
			t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
				authID := t.Name()
				plugin := &captureEffectiveServiceTierPlugin{
					authID:  authID,
					records: make(chan usage.Record, 1),
				}
				usage.RegisterPlugin(plugin)

				auth := codexOAuthTestAuth(server.URL)
				auth.ID = authID
				executor := NewCodexExecutor(cfg)
				ctx := usage.WithServiceTier(context.Background(), usage.AutoServiceTier)
				req := cliproxyexecutor.Request{
					Model:   "gpt-6-astra",
					Payload: []byte(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"hi"}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAI,
					Stream:       stream,
				}
				if stream {
					result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream error: %v", errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error: %v", chunk.Err)
						}
					}
				} else if _, errExec := executor.Execute(ctx, auth, req, opts); errExec != nil {
					t.Fatalf("Execute error: %v", errExec)
				}

				if captured.serviceTier.String() != "priority" {
					t.Fatalf("upstream body service_tier = %q, want priority", captured.serviceTier.String())
				}
				record := <-plugin.records
				if record.ServiceTier != "auto" || record.EffectiveServiceTier != "priority" || record.ResponseServiceTier != "default" {
					t.Fatalf("usage tiers = requested:%q effective:%q response:%q, want auto/priority/default", record.ServiceTier, record.EffectiveServiceTier, record.ResponseServiceTier)
				}
			})
		}
	})

	t.Run("openai compat chat completions override", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if got := ojson.GetBytes(body, "service_tier").String(); got != "priority" {
				t.Errorf("upstream body service_tier = %q, want priority", got)
			}
			if r.Header.Get("Accept") == "text/event-stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"service_tier\":\"default\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.4","service_tier":"default","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}))
		defer server.Close()

		for _, stream := range []bool{false, true} {
			t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
				authID := t.Name()
				plugin := &captureEffectiveServiceTierPlugin{
					authID:  authID,
					records: make(chan usage.Record, 1),
				}
				usage.RegisterPlugin(plugin)

				auth := &cliproxyauth.Auth{
					ID:         authID,
					Provider:   "openai-compatibility",
					Attributes: map[string]string{"base_url": server.URL, "api_key": "sk-test"},
				}
				executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
				ctx := usage.WithServiceTier(context.Background(), usage.AutoServiceTier)
				req := cliproxyexecutor.Request{
					Model:   "gpt-5.4",
					Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAI,
					Stream:       stream,
				}
				if stream {
					result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream error: %v", errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error: %v", chunk.Err)
						}
					}
				} else if _, errExec := executor.Execute(ctx, auth, req, opts); errExec != nil {
					t.Fatalf("Execute error: %v", errExec)
				}

				record := <-plugin.records
				if record.ServiceTier != "auto" || record.EffectiveServiceTier != "priority" || record.ResponseServiceTier != "default" {
					t.Fatalf("usage tiers = requested:%q effective:%q response:%q, want auto/priority/default", record.ServiceTier, record.EffectiveServiceTier, record.ResponseServiceTier)
				}
			})
		}
	})

	t.Run("xai video get omits effective service tier while post records it", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/video.mp4","duration":6},"model":"grok-imagine-video","progress":100}`))
				return
			}
			_, _ = w.Write([]byte(`{"request_id":"vid_123"}`))
		}))
		defer server.Close()

		executor := NewXAIExecutor(cfg)
		for _, tc := range []struct {
			name    string
			payload string
			wantEff string
		}{
			{name: "post_generation", payload: `{"model":"grok-imagine-video","prompt":"animate"}`, wantEff: "priority"},
			{name: "get_status", payload: `{"request_id":"vid_123"}`, wantEff: ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				authID := t.Name()
				plugin := &captureEffectiveServiceTierPlugin{
					authID:  authID,
					records: make(chan usage.Record, 1),
				}
				usage.RegisterPlugin(plugin)

				auth := &cliproxyauth.Auth{
					ID:         authID,
					Provider:   "xai",
					Attributes: map[string]string{"base_url": server.URL},
					Metadata:   map[string]any{"access_token": "xai-token"},
				}
				ctx := usage.WithServiceTier(context.Background(), usage.AutoServiceTier)
				_, errExec := executor.Execute(ctx, auth, cliproxyexecutor.Request{
					Model:   "grok-imagine-video",
					Payload: []byte(tc.payload),
				}, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai-video"),
				})
				if errExec != nil {
					t.Fatalf("Execute error: %v", errExec)
				}
				record := <-plugin.records
				if record.ServiceTier != "auto" || record.EffectiveServiceTier != tc.wantEff {
					t.Fatalf("usage tiers = requested:%q effective:%q, want auto/%q", record.ServiceTier, record.EffectiveServiceTier, tc.wantEff)
				}
			})
		}
	})

	t.Run("devin omits unencoded effective service tier", func(t *testing.T) {
		var frames []byte
		frame := appendDevinFieldBytes(nil, 2, []byte("ok"))
		frames = append(frames, helps.WrapConnectEnvelope(frame)...)
		frames = append(frames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, errRead := helps.ReadConnectFrame(r.Body)
			if errRead != nil {
				t.Errorf("read upstream request: %v", errRead)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/connect+proto")
			_, _ = w.Write(frames)
		}))
		defer server.Close()

		executor := NewDevinExecutor(cfg)
		for _, stream := range []bool{false, true} {
			t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
				authID := t.Name()
				plugin := &captureEffectiveServiceTierPlugin{
					authID:  authID,
					records: make(chan usage.Record, 1),
				}
				usage.RegisterPlugin(plugin)

				auth := &cliproxyauth.Auth{
					ID:         authID,
					Provider:   "devin",
					Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
				}
				ctx := usage.WithServiceTier(context.Background(), usage.AutoServiceTier)
				payload := []byte(`{"model":"devin/swe-2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
				req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: payload}
				opts := cliproxyexecutor.Options{
					SourceFormat:    sdktranslator.FormatClaude,
					OriginalRequest: payload,
					Stream:          stream,
				}
				if stream {
					result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream error: %v", errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error: %v", chunk.Err)
						}
					}
				} else if _, errExec := executor.Execute(ctx, auth, req, opts); errExec != nil {
					t.Fatalf("Execute error: %v", errExec)
				}
				record := <-plugin.records
				if record.ServiceTier != "auto" || record.EffectiveServiceTier != "" {
					t.Fatalf("usage tiers = requested:%q effective:%q, want auto/empty", record.ServiceTier, record.EffectiveServiceTier)
				}
			})
		}
	})
}

func TestPayloadBarrierMultiExecutorSingleDocConsistency(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "*", Protocol: "claude"},
						{Name: "*", Protocol: "codex"},
						{Name: "*", Protocol: "gemini"},
						{Name: "*", Protocol: "openai"},
					},
					Params: map[string]any{
						"custom_override": "barrier_won",
					},
				},
			},
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "*", Protocol: "claude"},
						{Name: "*", Protocol: "codex"},
						{Name: "*", Protocol: "gemini"},
						{Name: "*", Protocol: "openai"},
					},
					Params: []string{"filtered_out"},
				},
			},
		},
	}

	t.Run("codex cacheHelperDoc applies FinalizePayloadDoc as final barrier", func(t *testing.T) {
		exec := NewCodexExecutor(cfg)
		raw := []byte(`{"model":"gpt-5","filtered_out":"drop_me","input":[{"type":"message","role":"user","id":"item_1","content":[{"type":"input_text","text":"hi"}]}]}`)
		req := cliproxyexecutor.Request{Model: "gpt-5", Payload: raw}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
		reqCtx := helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(cfg, exec.Identifier(), "gpt-5", "codex", "", raw, req, opts))

		doc := ojson.NewDoc(bytes.Clone(raw))
		_, out, errCache := exec.cacheHelperDoc(reqCtx, opts.SourceFormat, "https://chatgpt.com/backend-api/codex/responses", req, doc, nil, opts.Headers)
		if errCache != nil {
			t.Fatalf("cacheHelperDoc error: %v", errCache)
		}
		if got := ojson.GetBytesNoCopy(out, "custom_override").String(); got != "barrier_won" {
			t.Fatalf("custom_override = %q, want barrier_won", got)
		}
		if ojson.GetBytesNoCopy(out, "filtered_out").Exists() {
			t.Fatalf("filtered_out should be removed by finalizer: %s", out)
		}
	})

	t.Run("xai prepared request doc applies FinalizeDoc as final barrier", func(t *testing.T) {
		exec := NewXAIExecutor(cfg)
		raw := []byte(`{"model":"grok-4","filtered_out":"drop_me","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
		req := cliproxyexecutor.Request{Model: "grok-4", Payload: raw}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
		prepared, errPrep := exec.prepareResponsesRequest(ctx, req, opts, true)
		if errPrep != nil {
			t.Fatalf("prepareResponsesRequest error: %v", errPrep)
		}
		sanitizeXAIInputEncryptedContentForBaseURLDoc(prepared.doc, "")
		prepared.finalizePayload.FinalizeDoc(prepared.doc)
		out := prepared.doc.Bytes()
		if got := ojson.GetBytesNoCopy(out, "custom_override").String(); got != "barrier_won" {
			t.Fatalf("custom_override = %q, want barrier_won", got)
		}
		if ojson.GetBytesNoCopy(out, "filtered_out").Exists() {
			t.Fatalf("filtered_out should be removed by finalizer: %s", out)
		}
	})
}

const payloadRuleProbeKey = "payload_rule_probe"

// payloadRuleProbeConfig returns a config whose rules apply to every model: an override that adds
// payloadRuleProbeKey and a filter that removes the given field.
func payloadRuleProbeConfig(filtered string) *config.Config {
	models := []config.PayloadModelRule{{Name: "*"}}
	return &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: models, Params: map[string]any{payloadRuleProbeKey: "configured"}}},
		Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{filtered}}},
	}}
}

// newPayloadCaptureServer starts an upstream that records the body of every request except the
// service-account token exchange and answers through respond. The returned func reports the body
// of the most recent captured request.
func newPayloadCaptureServer(t *testing.T, respond http.HandlerFunc) (*httptest.Server, func() []byte) {
	t.Helper()
	var mu sync.Mutex
	var last []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"probe-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream request body: %v", errRead)
			return
		}
		mu.Lock()
		last = body
		mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	return server, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return bytes.Clone(last)
	}
}

// assertPayloadRulesApplyOnPath checks that user payload rules reach the upstream on one request
// path. run performs the request against a fresh executor built from cfg and returns the body the
// upstream received. A control run without rules first proves the filtered field is really sent,
// so its later absence can only come from the filter rule.
func assertPayloadRulesApplyOnPath(t *testing.T, filtered string, run func(cfg *config.Config) []byte) {
	t.Helper()
	control := run(&config.Config{})
	if !ojson.GetBytes(control, filtered).Exists() || ojson.GetBytes(control, payloadRuleProbeKey).Exists() {
		t.Fatalf("control request without rules must carry %q and no probe key: %s", filtered, control)
	}
	got := run(payloadRuleProbeConfig(filtered))
	if value := ojson.GetBytes(got, payloadRuleProbeKey).String(); value != "configured" {
		t.Fatalf("override %q = %q, want configured: %s", payloadRuleProbeKey, value, got)
	}
	if ojson.GetBytes(got, filtered).Exists() {
		t.Fatalf("filtered field %q reached the upstream: %s", filtered, got)
	}
}

// Local token counters never reach an upstream, so the rules are checked through the count: the
// count with rules must equal the count of a rule-free request that literally carries the final
// content, and must differ from the count of the request as the client sent it.
func TestPayloadBarrierLocalTokenCountsApplyPayloadRules(t *testing.T) {
	const longText = "a much longer configured prompt that is certain to take more tokens than the short original one it replaces"
	messageItem := func(text string) string {
		return `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}`
	}
	tests := []struct {
		name      string
		model     string
		format    sdktranslator.Format
		countPath string
		override  map[string]any
		filter    string
		original  string
		final     string
		count     func(cfg *config.Config, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	}{
		{
			name:      "openai compat",
			model:     "gpt-5.4",
			format:    sdktranslator.FormatOpenAI,
			countPath: "usage.prompt_tokens",
			override:  map[string]any{"messages.0.content": longText},
			filter:    "messages.1",
			original:  `{"messages":[{"role":"user","content":"hi"},{"role":"user","content":"dropped second turn"}]}`,
			final:     `{"messages":[{"role":"user","content":"` + longText + `"}]}`,
			count: func(cfg *config.Config, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return NewOpenAICompatExecutor("openai-compatibility", cfg).CountTokens(context.Background(), nil, req, opts)
			},
		},
		{
			name:      "codex",
			model:     "gpt-5.4",
			format:    sdktranslator.FormatOpenAIResponse,
			countPath: "response.usage.input_tokens",
			override:  map[string]any{"instructions": longText},
			filter:    "input.1",
			original:  `{"input":[` + messageItem("hi") + `,` + messageItem("dropped second turn") + `]}`,
			final:     `{"instructions":"` + longText + `","input":[` + messageItem("hi") + `]}`,
			count: func(cfg *config.Config, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return NewCodexExecutor(cfg).CountTokens(context.Background(), nil, req, opts)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := func(cfg *config.Config, payload string) int64 {
				t.Helper()
				resp, errCount := tt.count(cfg, cliproxyexecutor.Request{Model: tt.model, Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: tt.format})
				if errCount != nil {
					t.Fatalf("CountTokens() error = %v", errCount)
				}
				value := ojson.GetBytes(resp.Payload, tt.countPath)
				if !value.Exists() || value.Int() <= 0 {
					t.Fatalf("missing token count at %s: %s", tt.countPath, resp.Payload)
				}
				return value.Int()
			}
			models := []config.PayloadModelRule{{Name: "*"}}
			rules := &config.Config{Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{Models: models, Params: tt.override}},
				Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{tt.filter}}},
			}}

			unruled := count(&config.Config{}, tt.original)
			want := count(&config.Config{}, tt.final)
			if want == unruled {
				t.Fatalf("fixture cannot detect skipped rules: final and original both count %d", want)
			}
			if got := count(rules, tt.original); got != want {
				t.Fatalf("count with payload rules = %d, want %d (rule-free count of the final payload; unmodified request counts %d)", got, want, unruled)
			}
		})
	}
}
