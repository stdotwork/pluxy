package cmd

import (
	"context"
	"encoding/json"
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

// StartService initializes and starts the main API proxy service.
// It loads all available authentication tokens, creates a pool of clients,
// starts the API server, and handles graceful shutdown signals.
func StartService(cfg *config.Config) {
	// Create a pool of API clients, one for each token file found.
	cliClients := make([]*client.Client, 0)
	err := filepath.Walk(cfg.AuthDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Process only JSON files in the auth directory.
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			log.Debugf("Loading token from: %s", path)
			f, errOpen := os.Open(path)
			if errOpen != nil {
				return errOpen
			}
			defer func() {
				_ = f.Close()
			}()

			// Decode the token storage file.
			var ts auth.TokenStorage
			if err = json.NewDecoder(f).Decode(&ts); err == nil {
				// For each valid token, create an authenticated client.
				clientCtx := context.Background()
				log.Info("Initializing authentication for token...")
				httpClient, errGetClient := auth.GetAuthenticatedClient(clientCtx, &ts, cfg)
				if errGetClient != nil {
					// Log fatal will exit, but we return the error for completeness.
					log.Fatalf("failed to get authenticated client for token %s: %v", path, errGetClient)
					return errGetClient
				}
				log.Info("Authentication successful.")

				// Add the new client to the pool.
				cliClient := client.NewClient(httpClient, &ts, cfg)
				cliClients = append(cliClients, cliClient)
			}
		}
		return nil
	})
	if err != nil {
		log.Fatalf("Error walking auth directory: %v", err)
	}

	// Create and start the API server with the pool of clients.
	apiServer := api.NewServer(cfg, cliClients)
	log.Infof("Starting API server on port %d", cfg.Port)
	if err = apiServer.Start(); err != nil {
		log.Fatalf("API server failed to start: %v", err)
	}

	// Set up a channel to listen for OS signals for graceful shutdown.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Main loop to wait for shutdown signal.
	for {
		select {
		case <-sigChan:
			log.Debugf("Received shutdown signal. Cleaning up...")

			// Create a context with a timeout for the shutdown process.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = cancel

			// Stop the API server gracefully.
			if err = apiServer.Stop(ctx); err != nil {
				log.Debugf("Error stopping API server: %v", err)
			}

			log.Debugf("Cleanup completed. Exiting...")
			os.Exit(0)
		case <-time.After(5 * time.Second):
			// This case is currently empty and acts as a periodic check.
			// It could be used for periodic tasks in the future.
		}
	}
}
