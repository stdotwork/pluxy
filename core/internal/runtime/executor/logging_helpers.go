package executor

import (
	"bytes"
	"context"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// recordAPIRequest stores the upstream request payload in Gin context for request logging.
func recordAPIRequest(ctx context.Context, cfg *config.Config, payload []byte) {
	if cfg == nil || !cfg.RequestLog || len(payload) == 0 {
		return
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		ginCtx.Set("API_REQUEST", bytes.Clone(payload))
	}
}

// appendAPIResponseChunk appends an upstream response chunk to Gin context for request logging.
func appendAPIResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	if cfg == nil || !cfg.RequestLog {
		return
	}
	data := bytes.TrimSpace(bytes.Clone(chunk))
	if len(data) == 0 {
		return
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		if existing, exists := ginCtx.Get("API_RESPONSE"); exists {
			if prev, okBytes := existing.([]byte); okBytes {
				prev = append(prev, data...)
				prev = append(prev, []byte("\n\n")...)
				ginCtx.Set("API_RESPONSE", prev)
				return
			}
		}
		ginCtx.Set("API_RESPONSE", data)
	}
}
