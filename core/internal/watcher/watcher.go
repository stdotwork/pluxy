// Package watcher provides file system monitoring functionality for the CLI Proxy API.
// It watches configuration files and authentication directories for changes,
// automatically reloading clients and configuration when files are modified.
// The package handles cross-platform file system events and supports hot-reloading.
package watcher

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/luispater/CLIProxyAPI/internal/auth/claude"
	"github.com/luispater/CLIProxyAPI/internal/auth/codex"
	"github.com/luispater/CLIProxyAPI/internal/auth/gemini"
	"github.com/luispater/CLIProxyAPI/internal/auth/qwen"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Watcher manages file watching for configuration and authentication files
type Watcher struct {
	configPath     string
	authDir        string
	config         *config.Config
	clients        []interfaces.Client
	clientsMutex   sync.RWMutex
	reloadCallback func([]interfaces.Client, *config.Config)
	watcher        *fsnotify.Watcher
}

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func([]interfaces.Client, *config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}

	return &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: reloadCallback,
		watcher:        watcher,
	}, nil
}

// Start begins watching the configuration file and authentication directory
func (w *Watcher) Start(ctx context.Context) error {
	// Watch the config file
	if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
		log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
		return errAddConfig
	}
	log.Debugf("watching config file: %s", w.configPath)

	// Watch the auth directory
	if errAddAuthDir := w.watcher.Add(w.authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", w.authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory: %s", w.authDir)

	// Start the event processing goroutine
	go w.processEvents(ctx)

	return nil
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	return w.watcher.Close()
}

// SetConfig updates the current configuration
func (w *Watcher) SetConfig(cfg *config.Config) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cfg
}

// SetClients updates the current client list
func (w *Watcher) SetClients(clients []interfaces.Client) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.clients = clients
}

// processEvents handles file system events
func (w *Watcher) processEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case errWatch, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Errorf("file watcher error: %v", errWatch)
		}
	}
}

// handleEvent processes individual file system events
func (w *Watcher) handleEvent(event fsnotify.Event) {
	now := time.Now()

	log.Debugf("file system event detected: %s %s", event.Op.String(), event.Name)

	// Handle config file changes
	if event.Name == w.configPath && (event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
		log.Infof("config file changed, reloading: %s", w.configPath)
		log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
		w.reloadConfig()
		return
	}

	// Handle auth directory changes (only for .json files)
	// Simplified: reload on any change to .json files in auth directory
	if strings.HasPrefix(event.Name, w.authDir) && strings.HasSuffix(event.Name, ".json") {
		log.Infof("auth file changed (%s): %s, reloading clients", event.Op.String(), filepath.Base(event.Name))
		log.Debugf("auth file change details - operation: %s, file: %s, timestamp: %s",
			event.Op.String(), filepath.Base(event.Name), now.Format("2006-01-02 15:04:05.000"))
		w.reloadClients()
	}
}

// reloadConfig reloads the configuration and triggers a full reload
func (w *Watcher) reloadConfig() {
	log.Debugf("starting config reload from: %s", w.configPath)

	newConfig, errLoadConfig := config.LoadConfig(w.configPath)
	if errLoadConfig != nil {
		log.Errorf("failed to reload config: %v", errLoadConfig)
		return
	}

	w.clientsMutex.Lock()
	oldConfig := w.config
	w.config = newConfig
	w.clientsMutex.Unlock()

	// Log configuration changes in debug mode
	if oldConfig != nil {
		log.Debugf("config changes detected:")
		if oldConfig.Port != newConfig.Port {
			log.Debugf("  port: %d -> %d", oldConfig.Port, newConfig.Port)
		}
		if oldConfig.AuthDir != newConfig.AuthDir {
			log.Debugf("  auth-dir: %s -> %s", oldConfig.AuthDir, newConfig.AuthDir)
		}
		if oldConfig.Debug != newConfig.Debug {
			log.Debugf("  debug: %t -> %t", oldConfig.Debug, newConfig.Debug)
		}
		if oldConfig.ProxyURL != newConfig.ProxyURL {
			log.Debugf("  proxy-url: %s -> %s", oldConfig.ProxyURL, newConfig.ProxyURL)
		}
		if oldConfig.RequestLog != newConfig.RequestLog {
			log.Debugf("  request-log: %t -> %t", oldConfig.RequestLog, newConfig.RequestLog)
		}
		if oldConfig.RequestRetry != newConfig.RequestRetry {
			log.Debugf("  request-retry: %d -> %d", oldConfig.RequestRetry, newConfig.RequestRetry)
		}
		if len(oldConfig.APIKeys) != len(newConfig.APIKeys) {
			log.Debugf("  api-keys count: %d -> %d", len(oldConfig.APIKeys), len(newConfig.APIKeys))
		}
		if len(oldConfig.GlAPIKey) != len(newConfig.GlAPIKey) {
			log.Debugf("  generative-language-api-key count: %d -> %d", len(oldConfig.GlAPIKey), len(newConfig.GlAPIKey))
		}
		if len(oldConfig.ClaudeKey) != len(newConfig.ClaudeKey) {
			log.Debugf("  claude-api-key count: %d -> %d", len(oldConfig.ClaudeKey), len(newConfig.ClaudeKey))
		}
		if oldConfig.AllowLocalhostUnauthenticated != newConfig.AllowLocalhostUnauthenticated {
			log.Debugf("  allow-localhost-unauthenticated: %t -> %t", oldConfig.AllowLocalhostUnauthenticated, newConfig.AllowLocalhostUnauthenticated)
		}
		if oldConfig.RemoteManagement.AllowRemote != newConfig.RemoteManagement.AllowRemote {
			log.Debugf("  remote-management.allow-remote: %t -> %t", oldConfig.RemoteManagement.AllowRemote, newConfig.RemoteManagement.AllowRemote)
		}
	}

	log.Infof("config successfully reloaded, triggering client reload")
	// Reload clients with new config
	w.reloadClients()
}

// reloadClients reloads all authentication clients
func (w *Watcher) reloadClients() {
	log.Debugf("starting client reload process")

	w.clientsMutex.RLock()
	cfg := w.config
	oldClientCount := len(w.clients)
	w.clientsMutex.RUnlock()

	if cfg == nil {
		log.Error("config is nil, cannot reload clients")
		return
	}

	log.Debugf("scanning auth directory: %s", cfg.AuthDir)

	// Create new client list
	newClients := make([]interfaces.Client, 0)
	authFileCount := 0
	successfulAuthCount := 0

	if strings.HasPrefix(cfg.AuthDir, "~") {
		home, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			log.Fatalf("failed to get home directory: %v", errUserHomeDir)
		}
		// Reconstruct the path by replacing the tilde with the user's home directory.
		parts := strings.Split(cfg.AuthDir, string(os.PathSeparator))
		if len(parts) > 1 {
			parts[0] = home
			cfg.AuthDir = path.Join(parts...)
		} else {
			// If the path is just "~", set it to the home directory.
			cfg.AuthDir = home
		}
	}

	// Load clients from auth directory
	errWalk := filepath.Walk(cfg.AuthDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			log.Debugf("error accessing path %s: %v", path, err)
			return err
		}

		// Process only JSON files in the auth directory
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			authFileCount++
			log.Debugf("processing auth file %d: %s", authFileCount, filepath.Base(path))

			data, errReadFile := os.ReadFile(path)
			if errReadFile != nil {
				return errReadFile
			}

			tokenType := "gemini"
			typeResult := gjson.GetBytes(data, "type")
			if typeResult.Exists() {
				tokenType = typeResult.String()
			}

			// Decode the token storage file
			if tokenType == "gemini" {
				var ts gemini.GeminiTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid token, create an authenticated client
					clientCtx := context.Background()
					log.Debugf("  initializing gemini authentication for token from %s...", filepath.Base(path))
					geminiAuth := gemini.NewGeminiAuth()
					httpClient, errGetClient := geminiAuth.GetAuthenticatedClient(clientCtx, &ts, cfg)
					if errGetClient != nil {
						log.Errorf("  failed to get authenticated client for token %s: %v", path, errGetClient)
						return nil // Continue processing other files
					}
					log.Debugf("  authentication successful for token from %s", filepath.Base(path))

					// Add the new client to the pool
					cliClient := client.NewGeminiCLIClient(httpClient, &ts, cfg)
					newClients = append(newClients, cliClient)
					successfulAuthCount++
				} else {
					log.Errorf("  failed to decode token file %s: %v", path, err)
				}
			} else if tokenType == "codex" {
				var ts codex.CodexTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid token, create an authenticated client
					log.Debugf("  initializing codex authentication for token from %s...", filepath.Base(path))
					codexClient, errGetClient := client.NewCodexClient(cfg, &ts)
					if errGetClient != nil {
						log.Errorf("  failed to get authenticated client for token %s: %v", path, errGetClient)
						return nil // Continue processing other files
					}
					log.Debugf("  authentication successful for token from %s", filepath.Base(path))

					// Add the new client to the pool
					newClients = append(newClients, codexClient)
					successfulAuthCount++
				} else {
					log.Errorf("  failed to decode token file %s: %v", path, err)
				}
			} else if tokenType == "claude" {
				var ts claude.ClaudeTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid token, create an authenticated client
					log.Debugf("  initializing claude authentication for token from %s...", filepath.Base(path))
					claudeClient := client.NewClaudeClient(cfg, &ts)
					log.Debugf("  authentication successful for token from %s", filepath.Base(path))

					// Add the new client to the pool
					newClients = append(newClients, claudeClient)
					successfulAuthCount++
				} else {
					log.Errorf("  failed to decode token file %s: %v", path, err)
				}
			} else if tokenType == "qwen" {
				var ts qwen.QwenTokenStorage
				if err = json.Unmarshal(data, &ts); err == nil {
					// For each valid token, create an authenticated client
					log.Debugf("  initializing qwen authentication for token from %s...", filepath.Base(path))
					qwenClient := client.NewQwenClient(cfg, &ts)
					log.Debugf("  authentication successful for token from %s", filepath.Base(path))

					// Add the new client to the pool
					newClients = append(newClients, qwenClient)
					successfulAuthCount++
				} else {
					log.Errorf("  failed to decode token file %s: %v", path, err)
				}
			}
		}
		return nil
	})
	if errWalk != nil {
		log.Errorf("error walking auth directory: %v", errWalk)
		return
	}

	log.Debugf("auth directory scan complete - found %d .json files, %d successful authentications", authFileCount, successfulAuthCount)

	// Add clients for Generative Language API keys if configured
	glAPIKeyCount := 0
	if len(cfg.GlAPIKey) > 0 {
		log.Debugf("processing %d Generative Language API Keys", len(cfg.GlAPIKey))
		for i := 0; i < len(cfg.GlAPIKey); i++ {
			httpClient := util.SetProxy(cfg, &http.Client{})

			log.Debugf("Initializing with Generative Language API Key %d...", i+1)
			cliClient := client.NewGeminiClient(httpClient, cfg, cfg.GlAPIKey[i])
			newClients = append(newClients, cliClient)
			glAPIKeyCount++
		}
		log.Debugf("Successfully initialized %d Generative Language API Key clients", glAPIKeyCount)
	}

	claudeAPIKeyCount := 0
	if len(cfg.ClaudeKey) > 0 {
		log.Debugf("processing %d Claude API Keys", len(cfg.ClaudeKey))
		for i := 0; i < len(cfg.ClaudeKey); i++ {
			log.Debugf("Initializing with Claude API Key %d...", i+1)
			cliClient := client.NewClaudeClientWithKey(cfg, i)
			newClients = append(newClients, cliClient)
			claudeAPIKeyCount++
		}
		log.Debugf("Successfully initialized %d Claude API Key clients", claudeAPIKeyCount)
	}

	// Add clients for OpenAI compatibility providers if configured
	openAICompatCount := 0
	if len(cfg.OpenAICompatibility) > 0 {
		log.Debugf("processing %d OpenAI-compatibility providers", len(cfg.OpenAICompatibility))
		for i := 0; i < len(cfg.OpenAICompatibility); i++ {
			compat := cfg.OpenAICompatibility[i]
			compatClient, errClient := client.NewOpenAICompatibilityClient(cfg, &compat)
			if errClient != nil {
				log.Errorf("  failed to create OpenAI-compatibility client for %s: %v", compat.Name, errClient)
				continue
			}
			newClients = append(newClients, compatClient)
			openAICompatCount++
		}
		log.Debugf("Successfully initialized %d OpenAI-compatibility clients", openAICompatCount)
	}

	// Unregister old clients from the model registry if supported
	w.clientsMutex.RLock()
	for i := 0; i < len(w.clients); i++ {
		if u, ok := any(w.clients[i]).(interface{ UnregisterClient() }); ok {
			u.UnregisterClient()
		}
	}
	w.clientsMutex.RUnlock()

	// Update the client list
	w.clientsMutex.Lock()
	w.clients = newClients
	w.clientsMutex.Unlock()

	log.Infof("client reload complete - old: %d clients, new: %d clients (%d auth files + %d GL API keys + %d Claude API keys + %d OpenAI-compat)",
		oldClientCount,
		len(newClients),
		successfulAuthCount,
		glAPIKeyCount,
		claudeAPIKeyCount,
		openAICompatCount,
	)

	// Trigger the callback to update the server
	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback")
		w.reloadCallback(newClients, cfg)
	}
}
