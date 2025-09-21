package cmd

import (
	"context"
	"errors"
	"os/signal"
	"syscall"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	log "github.com/sirupsen/logrus"
)

// StartService builds and runs the proxy service using the exported SDK.
func StartService(cfg *config.Config, configPath string) {
	service, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		Build()
	if err != nil {
		log.Fatalf("failed to build proxy service: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	err = service.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("proxy service exited with error: %v", err)
	}
}
