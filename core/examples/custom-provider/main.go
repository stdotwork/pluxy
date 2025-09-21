package main

// Example: Custom provider executor + translators embedded in the SDK server.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	api "github.com/router-for-me/CLIProxyAPI/v6/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktr "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

const (
	providerKey = "myprov"
	fOpenAI     = sdktr.Format("openai.chat")
	fMyProv     = sdktr.Format("myprov.chat")
)

// Register trivial translators (pass-through demo).
func init() {
	sdktr.Register(fOpenAI, fMyProv,
		func(model string, raw []byte, stream bool) []byte { return raw },
		sdktr.ResponseTransform{
			Stream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) []string {
				return []string{string(raw)}
			},
			NonStream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) string {
				return string(raw)
			},
		},
	)
}

// MyExecutor is a minimal provider implementation for demo purposes.
type MyExecutor struct{}

func (MyExecutor) Identifier() string { return providerKey }

// PrepareRequest optionally injects credentials to raw HTTP requests.
func (MyExecutor) PrepareRequest(req *http.Request, a *coreauth.Auth) error {
	if req == nil || a == nil {
		return nil
	}
	if a.Attributes != nil {
		if ak := strings.TrimSpace(a.Attributes["api_key"]); ak != "" {
			req.Header.Set("Authorization", "Bearer "+ak)
		}
	}
	return nil
}

func buildHTTPClient(a *coreauth.Auth) *http.Client {
	if a == nil || strings.TrimSpace(a.ProxyURL) == "" {
		return http.DefaultClient
	}
	u, err := url.Parse(a.ProxyURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return http.DefaultClient
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func upstreamEndpoint(a *coreauth.Auth) string {
	if a != nil && a.Attributes != nil {
		if ep := strings.TrimSpace(a.Attributes["endpoint"]); ep != "" {
			return ep
		}
	}
	// Demo echo endpoint; replace with your upstream.
	return "https://httpbin.org/post"
}

func (MyExecutor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	client := buildHTTPClient(a)
	endpoint := upstreamEndpoint(a)

	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(req.Payload))
	if errNew != nil {
		return clipexec.Response{}, errNew
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Inject credentials via PrepareRequest hook.
	_ = (MyExecutor{}).PrepareRequest(httpReq, a)

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return clipexec.Response{}, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			// Best-effort close; log if needed in real projects.
		}
	}()
	body, _ := io.ReadAll(resp.Body)
	return clipexec.Response{Payload: body}, nil
}

func (MyExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (<-chan clipexec.StreamChunk, error) {
	ch := make(chan clipexec.StreamChunk, 1)
	go func() {
		defer close(ch)
		ch <- clipexec.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
	}()
	return ch, nil
}

func (MyExecutor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}

func main() {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		panic(err)
	}

	core := coreauth.NewManager(coreauth.NewFileStore(cfg.AuthDir), nil, nil)
	core.RegisterExecutor(MyExecutor{})

	hooks := cliproxy.Hooks{
		OnAfterStart: func(s *cliproxy.Service) {
			// Register demo models for the custom provider so they appear in /v1/models.
			models := []*cliproxy.ModelInfo{{ID: "myprov-pro-1", Object: "model", Type: providerKey, DisplayName: "MyProv Pro 1"}}
			for _, a := range core.List() {
				if strings.EqualFold(a.Provider, providerKey) {
					cliproxy.GlobalModelRegistry().RegisterClient(a.ID, providerKey, models)
				}
			}
		},
	}

	svc, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath("config.yaml").
		WithCoreAuthManager(core).
		WithServerOptions(
			// Optional: add a simple middleware + custom request logger
			api.WithMiddleware(func(c *gin.Context) { c.Header("X-Example", "custom-provider"); c.Next() }),
			api.WithRequestLoggerFactory(func(cfg *config.Config, cfgPath string) logging.RequestLogger {
				return logging.NewFileRequestLogger(true, "logs", filepath.Dir(cfgPath))
			}),
		).
		WithHooks(hooks).
		Build()
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
	_ = os.Stderr // keep os import used (demo only)
	_ = time.Second
}
