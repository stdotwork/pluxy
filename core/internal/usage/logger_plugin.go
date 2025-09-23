package usage

import (
	"context"
	"encoding/json"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

func init() {
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin outputs every usage record to the application log.
type LoggerPlugin struct{}

// NewLoggerPlugin constructs a new logger plugin instance.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{} }

// HandleUsage implements coreusage.Plugin.
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	// Output all relevant fields for observability; keep logging lightweight and non-blocking.
	data, _ := json.Marshal(record)
	log.Debug(string(data))
}
