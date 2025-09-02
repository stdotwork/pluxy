// Package cmd provides command-line interface functionality for the CLI Proxy API.
// It implements the main application commands including service startup, authentication
// client management, and graceful shutdown handling. The package handles loading
// authentication tokens, creating client pools, starting the API server, and monitoring
// configuration changes through file watchers.
package cmd

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/luispater/CLIProxyAPI/internal/api"
	"github.com/luispater/CLIProxyAPI/internal/auth/claude"
	"github.com/luispater/CLIProxyAPI/internal/auth/codex"
	"github.com/luispater/CLIProxyAPI/internal/auth/gemini"
	"github.com/luispater/CLIProxyAPI/internal/auth/qwen"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/util"
	"github.com/luispater/CLIProxyAPI/internal/watcher"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// StartService initializes and starts the main API proxy service.
// It loads all available authentication tokens, creates a pool of clients,
// starts the API server, and handles graceful shutdown signals.
// The function performs the following operations:
// 1. Walks through the authentication directory to load all JSON token files
// 2. Creates authenticated clients based on token types (gemini, codex, claude, qwen)
// 3. Initializes clients with API keys if provided in configuration
// 4. Starts the API server with the client pool
// 5. Sets up file watching for configuration and authentication directory changes
// 6. Implements background token refresh for Codex, Claude, and Qwen clients
// 7. Handles graceful shutdown on SIGINT or SIGTERM signals
//
// Parameters:
//   - cfg: The application configuration containing settings like port, auth directory, API keys
//   - configPath: The path to the configuration file for watching changes
func StartService(cfg *config.Config, configPath string) {
	// Create a pool of API clients, one for each token file found.
	cliClients := make([]interfaces.Client, 0)
	err := filepath.Walk(cfg.AuthDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Process only JSON files in the auth directory to load authentication tokens.
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			log.Debugf("Loading token from: %s", path)
			data, errReadFile := os.ReadFile(path)
			if errReadFile != nil {
				return errReadFile
			}

			// Determine token type from JSON data, defaulting to "gemini" if not specified.
			tokenType := "gemini"
			typeResult := gjson.GetBytes(data, "type")
			if typeResult.Exists() {
				tokenType = typeResult.String()
			}

			clientCtx := context.Background()

			if tokenType == "gemini" {
				var ts gemini.GeminiTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid Gemini token, create an authenticated client.
					log.Info("Initializing gemini authentication for token...")
					geminiAuth := gemini.NewGeminiAuth()
					httpClient, errGetClient := geminiAuth.GetAuthenticatedClient(clientCtx, &ts, cfg)
					if errGetClient != nil {
						// Log fatal will exit, but we return the error for completeness.
						log.Fatalf("failed to get authenticated client for token %s: %v", path, errGetClient)
						return errGetClient
					}
					log.Info("Authentication successful.")

					// Add the new client to the pool.
					cliClient := client.NewGeminiCLIClient(httpClient, &ts, cfg)
					cliClients = append(cliClients, cliClient)
				}
			} else if tokenType == "codex" {
				var ts codex.CodexTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid Codex token, create an authenticated client.
					log.Info("Initializing codex authentication for token...")
					codexClient, errGetClient := client.NewCodexClient(cfg, &ts)
					if errGetClient != nil {
						// Log fatal will exit, but we return the error for completeness.
						log.Fatalf("failed to get authenticated client for token %s: %v", path, errGetClient)
						return errGetClient
					}
					log.Info("Authentication successful.")
					cliClients = append(cliClients, codexClient)
				}
			} else if tokenType == "claude" {
				var ts claude.ClaudeTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid Claude token, create an authenticated client.
					log.Info("Initializing claude authentication for token...")
					claudeClient := client.NewClaudeClient(cfg, &ts)
					log.Info("Authentication successful.")
					cliClients = append(cliClients, claudeClient)
				}
			} else if tokenType == "qwen" {
				var ts qwen.QwenTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid Qwen token, create an authenticated client.
					log.Info("Initializing qwen authentication for token...")
					qwenClient := client.NewQwenClient(cfg, &ts)
					log.Info("Authentication successful.")
					cliClients = append(cliClients, qwenClient)
				}
			}
		}
		return nil
	})
	if err != nil {
		log.Fatalf("Error walking auth directory: %v", err)
	}

	if len(cfg.GlAPIKey) > 0 {
		// Initialize clients with Generative Language API Keys if provided in configuration.
		for i := 0; i < len(cfg.GlAPIKey); i++ {
			httpClient := util.SetProxy(cfg, &http.Client{})

			log.Debug("Initializing with Generative Language API Key...")
			cliClient := client.NewGeminiClient(httpClient, cfg, cfg.GlAPIKey[i])
			cliClients = append(cliClients, cliClient)
		}
	}

	if len(cfg.ClaudeKey) > 0 {
		// Initialize clients with Claude API Keys if provided in configuration.
		for i := 0; i < len(cfg.ClaudeKey); i++ {
			log.Debug("Initializing with Claude API Key...")
			cliClient := client.NewClaudeClientWithKey(cfg, i)
			cliClients = append(cliClients, cliClient)
		}
	}

	if len(cfg.CodexKey) > 0 {
		// Initialize clients with Codex API Keys if provided in configuration.
		for i := 0; i < len(cfg.CodexKey); i++ {
			log.Debug("Initializing with Codex API Key...")
			cliClient := client.NewCodexClientWithKey(cfg, i)
			cliClients = append(cliClients, cliClient)
		}
	}

	if len(cfg.OpenAICompatibility) > 0 {
		// Initialize clients for OpenAI compatibility configurations
		for _, compatConfig := range cfg.OpenAICompatibility {
			log.Debugf("Initializing OpenAI compatibility client for provider: %s", compatConfig.Name)
			compatClient, errClient := client.NewOpenAICompatibilityClient(cfg, &compatConfig)
			if errClient != nil {
				log.Fatalf("failed to create OpenAI compatibility client for %s: %v", compatConfig.Name, errClient)
			}
			cliClients = append(cliClients, compatClient)
		}
	}

	// Create and start the API server with the pool of clients in a separate goroutine.
	apiServer := api.NewServer(cfg, cliClients, configPath)
	log.Infof("Starting API server on port %d", cfg.Port)

	// Start the API server in a goroutine so it doesn't block the main thread.
	go func() {
		if err = apiServer.Start(); err != nil {
			log.Fatalf("API server failed to start: %v", err)
		}
	}()

	// Give the server a moment to start up before proceeding.
	time.Sleep(100 * time.Millisecond)
	log.Info("API server started successfully")

	// Setup file watcher for config and auth directory changes to enable hot-reloading.
	fileWatcher, errNewWatcher := watcher.NewWatcher(configPath, cfg.AuthDir, func(newClients []interfaces.Client, newCfg *config.Config) {
		// Update the API server with new clients and configuration when files change.
		apiServer.UpdateClients(newClients, newCfg)
	})
	if errNewWatcher != nil {
		log.Fatalf("failed to create file watcher: %v", errNewWatcher)
	}

	// Set initial state for the watcher with current configuration and clients.
	fileWatcher.SetConfig(cfg)
	fileWatcher.SetClients(cliClients)

	// Start the file watcher in a separate context.
	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	if errStartWatcher := fileWatcher.Start(watcherCtx); errStartWatcher != nil {
		log.Fatalf("failed to start file watcher: %v", errStartWatcher)
	}
	log.Info("file watcher started for config and auth directory changes")

	defer func() {
		// Clean up file watcher resources on shutdown.
		watcherCancel()
		errStopWatcher := fileWatcher.Stop()
		if errStopWatcher != nil {
			log.Errorf("error stopping file watcher: %v", errStopWatcher)
		}
	}()

	// Set up a channel to listen for OS signals for graceful shutdown.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Background token refresh ticker for Codex, Claude, and Qwen clients to handle token expiration.
	ctxRefresh, cancelRefresh := context.WithCancel(context.Background())
	var wgRefresh sync.WaitGroup
	wgRefresh.Add(1)
	go func() {
		defer wgRefresh.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		// Function to check and refresh tokens for all client types before they expire.
		checkAndRefresh := func() {
			for i := 0; i < len(cliClients); i++ {
				if codexCli, ok := cliClients[i].(*client.CodexClient); ok {
					if ts, isCodexTS := codexCli.TokenStorage().(*claude.ClaudeTokenStorage); isCodexTS {
						if ts != nil && ts.Expire != "" {
							if expTime, errParse := time.Parse(time.RFC3339, ts.Expire); errParse == nil {
								if time.Until(expTime) <= 5*24*time.Hour {
									log.Debugf("refreshing codex tokens for %s", codexCli.GetEmail())
									_ = codexCli.RefreshTokens(ctxRefresh)
								}
							}
						}
					}
				} else if claudeCli, isOK := cliClients[i].(*client.ClaudeClient); isOK {
					if ts, isCluadeTS := claudeCli.TokenStorage().(*claude.ClaudeTokenStorage); isCluadeTS {
						if ts != nil && ts.Expire != "" {
							if expTime, errParse := time.Parse(time.RFC3339, ts.Expire); errParse == nil {
								if time.Until(expTime) <= 4*time.Hour {
									log.Debugf("refreshing claude tokens for %s", claudeCli.GetEmail())
									_ = claudeCli.RefreshTokens(ctxRefresh)
								}
							}
						}
					}
				} else if qwenCli, isQwenOK := cliClients[i].(*client.QwenClient); isQwenOK {
					if ts, isQwenTS := qwenCli.TokenStorage().(*qwen.QwenTokenStorage); isQwenTS {
						if ts != nil && ts.Expire != "" {
							if expTime, errParse := time.Parse(time.RFC3339, ts.Expire); errParse == nil {
								if time.Until(expTime) <= 3*time.Hour {
									log.Debugf("refreshing qwen tokens for %s", qwenCli.GetEmail())
									_ = qwenCli.RefreshTokens(ctxRefresh)
								}
							}
						}
					}
				}
			}
		}

		// Initial check on start to refresh tokens if needed.
		checkAndRefresh()
		for {
			select {
			case <-ctxRefresh.Done():
				log.Debugf("refreshing tokens stopped...")
				return
			case <-ticker.C:
				checkAndRefresh()
			}
		}
	}()

	// Main loop to wait for shutdown signal or periodic checks.
	for {
		select {
		case <-sigChan:
			log.Debugf("Received shutdown signal. Cleaning up...")

			cancelRefresh()
			wgRefresh.Wait()

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
			// Periodic check to keep the loop running.
		}
	}
}
