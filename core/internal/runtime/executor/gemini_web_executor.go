package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

type GeminiWebExecutor struct {
	cfg *config.Config
	mu  sync.Mutex
}

func NewGeminiWebExecutor(cfg *config.Config) *GeminiWebExecutor {
	return &GeminiWebExecutor{cfg: cfg}
}

func (e *GeminiWebExecutor) Identifier() string { return "gemini-web" }

func (e *GeminiWebExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *GeminiWebExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	state, err := e.stateFor(auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if err = state.ensureClient(); err != nil {
		return cliproxyexecutor.Response{}, err
	}

	mutex := state.getRequestMutex()
	if mutex != nil {
		mutex.Lock()
		defer mutex.Unlock()
	}

	payload := bytes.Clone(req.Payload)
	resp, errMsg, prep := state.send(ctx, req.Model, payload, opts)
	if errMsg != nil {
		return cliproxyexecutor.Response{}, geminiWebErrorFromMessage(errMsg)
	}
	resp = state.convertToTarget(ctx, req.Model, prep, resp)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-web")
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), payload, bytes.Clone(resp), &param)

	return cliproxyexecutor.Response{Payload: []byte(out)}, nil
}

func (e *GeminiWebExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	state, err := e.stateFor(auth)
	if err != nil {
		return nil, err
	}
	if err = state.ensureClient(); err != nil {
		return nil, err
	}

	mutex := state.getRequestMutex()
	if mutex != nil {
		mutex.Lock()
	}

	gemBytes, errMsg, prep := state.send(ctx, req.Model, bytes.Clone(req.Payload), opts)
	if errMsg != nil {
		if mutex != nil {
			mutex.Unlock()
		}
		return nil, geminiWebErrorFromMessage(errMsg)
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-web")
	var param any

	lines := state.convertStream(ctx, req.Model, prep, gemBytes)
	done := state.doneStream(ctx, req.Model, prep)
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		if mutex != nil {
			defer mutex.Unlock()
		}
		for _, line := range lines {
			line = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), req.Payload, bytes.Clone([]byte(line)), &param)
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(line)}
		}
		for _, line := range done {
			line = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), req.Payload, bytes.Clone([]byte(line)), &param)
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(line)}
		}
	}()
	return out, nil
}

func (e *GeminiWebExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte{}}, fmt.Errorf("not implemented")
}

func (e *GeminiWebExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("gemini web executor: refresh called")
	state, err := e.stateFor(auth)
	if err != nil {
		return nil, err
	}
	if err = state.refresh(ctx); err != nil {
		return nil, err
	}
	ts := state.tokenSnapshot()
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["secure_1psid"] = ts.Secure1PSID
	auth.Metadata["secure_1psidts"] = ts.Secure1PSIDTS
	auth.Metadata["type"] = "gemini-web"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	return auth, nil
}

type geminiWebRuntime struct {
	state *geminiWebState
}

func (e *GeminiWebExecutor) stateFor(auth *cliproxyauth.Auth) (*geminiWebState, error) {
	if auth == nil {
		return nil, fmt.Errorf("gemini-web executor: auth is nil")
	}
	if runtime, ok := auth.Runtime.(*geminiWebRuntime); ok && runtime != nil && runtime.state != nil {
		return runtime.state, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if runtime, ok := auth.Runtime.(*geminiWebRuntime); ok && runtime != nil && runtime.state != nil {
		return runtime.state, nil
	}

	ts, err := parseGeminiWebToken(auth)
	if err != nil {
		return nil, err
	}

	cfg := e.cfg
	if auth.ProxyURL != "" && cfg != nil {
		copyCfg := *cfg
		copyCfg.ProxyURL = auth.ProxyURL
		cfg = &copyCfg
	}

	storagePath := ""
	if auth.Attributes != nil {
		if p, ok := auth.Attributes["path"]; ok {
			storagePath = p
		}
	}
	state := newGeminiWebState(cfg, ts, storagePath)
	runtime := &geminiWebRuntime{state: state}
	auth.Runtime = runtime
	return state, nil
}

func parseGeminiWebToken(auth *cliproxyauth.Auth) (*gemini.GeminiWebTokenStorage, error) {
	if auth == nil {
		return nil, fmt.Errorf("gemini-web executor: auth is nil")
	}
	if auth.Metadata == nil {
		return nil, fmt.Errorf("gemini-web executor: missing metadata")
	}
	psid := stringFromMetadata(auth.Metadata, "secure_1psid", "secure_1psid", "__Secure-1PSID")
	psidts := stringFromMetadata(auth.Metadata, "secure_1psidts", "secure_1psidts", "__Secure-1PSIDTS")
	if psid == "" || psidts == "" {
		return nil, fmt.Errorf("gemini-web executor: incomplete cookie metadata")
	}
	return &gemini.GeminiWebTokenStorage{Secure1PSID: psid, Secure1PSIDTS: psidts}, nil
}

func stringFromMetadata(meta map[string]any, keys ...string) string {
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if s, okStr := val.(string); okStr && s != "" {
				return s
			}
		}
	}
	return ""
}

func geminiWebErrorFromMessage(msg *interfaces.ErrorMessage) error {
	if msg == nil {
		return nil
	}
	return geminiWebError{message: msg}
}

type geminiWebError struct {
	message *interfaces.ErrorMessage
}

func (e geminiWebError) Error() string {
	if e.message == nil {
		return "gemini-web error"
	}
	if e.message.Error != nil {
		return e.message.Error.Error()
	}
	return fmt.Sprintf("gemini-web error: status %d", e.message.StatusCode)
}

func (e geminiWebError) StatusCode() int {
	if e.message == nil {
		return 0
	}
	return e.message.StatusCode
}
