// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
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
// It implements the coreusage.Plugin interface to provide usage tracking
// and logging capabilities for monitoring API consumption.
type LoggerPlugin struct{}

// NewLoggerPlugin constructs a new logger plugin instance.
//
// Returns:
//   - *LoggerPlugin: A new logger plugin instance
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{} }

// HandleUsage implements coreusage.Plugin.
// It processes usage records by marshaling them to JSON and logging them
// at debug level for observability purposes.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to process and log
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	// Output all relevant fields for observability; keep logging lightweight and non-blocking.
	data, _ := json.Marshal(record)
	log.Debug(string(data))
}
