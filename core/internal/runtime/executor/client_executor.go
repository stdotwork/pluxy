package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// ClientAdapter bridges legacy stateful clients to the new ProviderExecutor contract.
type ClientAdapter struct {
	provider string
}

// NewClientAdapter creates a new adapter for the specified provider key.
func NewClientAdapter(provider string) *ClientAdapter {
	return &ClientAdapter{provider: provider}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (a *ClientAdapter) Identifier() string {
	return a.provider
}

// PrepareRequest implements optional request preparation hook (no-op for legacy clients).
func (a *ClientAdapter) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error { return nil }

// Execute implements cliproxyauth.ProviderExecutor.
func (a *ClientAdapter) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	client, mutex, err := resolveLegacyClient(auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	unlock := lock(mutex)
	defer unlock()

	// Support special actions via request metadata (e.g., countTokens)
	if req.Metadata != nil {
		if action, _ := req.Metadata["action"].(string); action == "countTokens" {
			if tc, ok := any(client).(interface {
				SendRawTokenCount(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage)
			}); ok {
				payload, errMsg := tc.SendRawTokenCount(ctx, req.Model, req.Payload, opts.Alt)
				if errMsg != nil {
					return cliproxyexecutor.Response{}, errorFromMessage(errMsg)
				}
				return cliproxyexecutor.Response{Payload: payload}, nil
			}
			return cliproxyexecutor.Response{}, fmt.Errorf("legacy client does not support countTokens")
		}
	}

	payload, errMsg := client.SendRawMessage(ctx, req.Model, req.Payload, opts.Alt)
	if errMsg != nil {
		return cliproxyexecutor.Response{}, errorFromMessage(errMsg)
	}
	return cliproxyexecutor.Response{Payload: payload}, nil
}

// ExecuteStream implements cliproxyauth.ProviderExecutor.
func (a *ClientAdapter) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	client, mutex, err := resolveLegacyClient(auth)
	if err != nil {
		return nil, err
	}
	unlock := lock(mutex)

	dataCh, errCh := client.SendRawMessageStream(ctx, req.Model, req.Payload, opts.Alt)
	if dataCh == nil {
		unlock()
		if errCh != nil {
			if msg := <-errCh; msg != nil {
				return nil, errorFromMessage(msg)
			}
		}
		return nil, errors.New("stream not available")
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer unlock()
		for chunk := range dataCh {
			if chunk == nil {
				continue
			}
			out <- cliproxyexecutor.StreamChunk{Payload: chunk}
		}
		if errCh != nil {
			if msg, ok := <-errCh; ok && msg != nil {
				out <- cliproxyexecutor.StreamChunk{Err: errorFromMessage(msg)}
			}
		}
	}()
	return out, nil
}

// Refresh delegates to the legacy client's refresh logic when available.
func (a *ClientAdapter) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	client, _, err := resolveLegacyClient(auth)
	if err != nil {
		return nil, err
	}
	if refresher, ok := client.(interface{ RefreshTokens(context.Context) error }); ok {
		if errRefresh := refresher.RefreshTokens(ctx); errRefresh != nil {
			return nil, errRefresh
		}
	}
	return auth, nil
}

// legacyClient defines the minimum surface required from the historical clients.
type legacyClient interface {
	SendRawMessage(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage)
	SendRawMessageStream(ctx context.Context, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage)
	GetRequestMutex() *sync.Mutex
}

func resolveLegacyClient(auth *cliproxyauth.Auth) (legacyClient, *sync.Mutex, error) {
	if auth == nil {
		return nil, nil, fmt.Errorf("legacy adapter: auth is nil")
	}
	client, ok := auth.Runtime.(legacyClient)
	if !ok || client == nil {
		return nil, nil, fmt.Errorf("legacy adapter: runtime client missing for %s", auth.ID)
	}
	return client, client.GetRequestMutex(), nil
}

func lock(mutex *sync.Mutex) func() {
	if mutex == nil {
		return func() {}
	}
	mutex.Lock()
	return func() {
		mutex.Unlock()
	}
}

func errorFromMessage(msg *interfaces.ErrorMessage) error {
	if msg == nil {
		return nil
	}
	return legacyError{message: msg}
}

type legacyError struct {
	message *interfaces.ErrorMessage
}

func (e legacyError) Error() string {
	if e.message == nil {
		return "legacy client error"
	}
	if e.message.Error != nil {
		return e.message.Error.Error()
	}
	return fmt.Sprintf("legacy client error: status %d", e.message.StatusCode)
}

// StatusCode implements executor.StatusError, exposing HTTP-like status.
func (e legacyError) StatusCode() int {
	if e.message != nil {
		return e.message.StatusCode
	}
	return 0
}

// UnwrapError extracts the legacy interfaces.ErrorMessage from adapter errors.
func UnwrapError(err error) (*interfaces.ErrorMessage, bool) {
	var legacy legacyError
	if errors.As(err, &legacy) {
		return legacy.message, true
	}
	return nil, false
}
