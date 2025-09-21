package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/sjson"
)

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey, baseURL := codexCreds(auth)
	if apiKey == "" {
		return NewClientAdapter("codex").Execute(ctx, auth, req, opts)
	}
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	if util.InArray([]string{"gpt-5-minimal", "gpt-5-low", "gpt-5-medium", "gpt-5-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5")
		switch req.Model {
		case "gpt-5-minimal":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "minimal")
		case "gpt-5-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	} else if util.InArray([]string{"gpt-5-codex", "gpt-5-codex-low", "gpt-5-codex-medium", "gpt-5-codex-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5-codex")
		switch req.Model {
		case "gpt-5-codex":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-codex-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-codex-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-codex-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	recordAPIRequest(ctx, e.cfg, body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{}
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		return cliproxyexecutor.Response{}, statusErr{code: resp.StatusCode, msg: string(b)}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	return cliproxyexecutor.Response{Payload: []byte(out)}, nil
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	apiKey, baseURL := codexCreds(auth)
	if apiKey == "" {
		return NewClientAdapter("codex").ExecuteStream(ctx, auth, req, opts)
	}
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	if util.InArray([]string{"gpt-5-minimal", "gpt-5-low", "gpt-5-medium", "gpt-5-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5")
		switch req.Model {
		case "gpt-5-minimal":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "minimal")
		case "gpt-5-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	} else if util.InArray([]string{"gpt-5-codex", "gpt-5-codex-low", "gpt-5-codex-medium", "gpt-5-codex-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5-codex")
		switch req.Model {
		case "gpt-5-codex":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-codex-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-codex-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-codex-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	recordAPIRequest(ctx, e.cfg, body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	httpClient := &http.Client{Timeout: 0}
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		return nil, statusErr{code: resp.StatusCode, msg: string(b)}
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 1024*1024)
		scanner.Buffer(buf, 1024*1024)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if err = scanner.Err(); err != nil {
			out <- cliproxyexecutor.StreamChunk{Err: err}
		}
	}()
	return out, nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	_ = ctx
	return auth, nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}
