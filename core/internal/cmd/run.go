package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/luispater/CLIProxyAPI/internal/api"
	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func StartService(cfg *config.Config) {
	// Create API server configuration
	apiConfig := &api.ServerConfig{
		Port:    fmt.Sprintf("%d", cfg.Port),
		Debug:   cfg.Debug,
		ApiKeys: cfg.ApiKeys,
	}

	cliClients := make([]*client.Client, 0)
	err := filepath.Walk(cfg.AuthDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			log.Debugf("Loading token from: %s", path)
			f, errOpen := os.Open(path)
			if errOpen != nil {
				return errOpen
			}
			defer func() {
				_ = f.Close()
			}()

			var ts auth.TokenStorage
			if err = json.NewDecoder(f).Decode(&ts); err == nil {
				// 2. Initialize authenticated HTTP Client
				clientCtx := context.Background()

				log.Info("Initializing authentication...")
				httpClient, errGetClient := auth.GetAuthenticatedClient(clientCtx, &ts, cfg)
				if errGetClient != nil {
					log.Fatalf("failed to get authenticated client: %v", errGetClient)
					return errGetClient
				}
				log.Info("Authentication successful.")

				// 3. Initialize CLI Client
				cliClient := client.NewClient(httpClient, &ts, cfg)
				cliClients = append(cliClients, cliClient)
			}
		}
		return nil
	})

	// Create API server
	apiServer := api.NewServer(apiConfig, cliClients)
	log.Infof("Starting API server on port %s", apiConfig.Port)
	if err = apiServer.Start(); err != nil {
		log.Fatalf("API server failed to start: %v", err)
		return
	}

	// Set up graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-sigChan:
			log.Debugf("Received shutdown signal. Cleaning up...")

			// Create shutdown context
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = ctx // Mark ctx as used to avoid error, as apiServer.Stop(ctx) is commented out

			// Stop API server
			if err = apiServer.Stop(ctx); err != nil {
				log.Debugf("Error stopping API server: %v", err)
			}
			cancel()

			log.Debugf("Cleanup completed. Exiting...")
			os.Exit(0)
		case <-time.After(5 * time.Second):

		}
	}
}
