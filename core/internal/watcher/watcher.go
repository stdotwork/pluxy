// Package watcher provides file system monitoring functionality for the CLI Proxy API.
// It watches configuration files and authentication directories for changes,
// automatically reloading clients and configuration when files are modified.
// The package handles cross-platform file system events and supports hot-reloading.
package watcher

import (
	"context"
	"encoding/json"
	"fmt"
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
	clients        map[string]interfaces.Client
	clientsMutex   sync.RWMutex
	reloadCallback func(map[string]interfaces.Client, *config.Config)
	watcher        *fsnotify.Watcher
	eventTimes     map[string]time.Time
	eventMutex     sync.Mutex
}

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func(map[string]interfaces.Client, *config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}

	return &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: reloadCallback,
		watcher:        watcher,
		clients:        make(map[string]interfaces.Client),
		eventTimes:     make(map[string]time.Time),
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
func (w *Watcher) SetClients(clients map[string]interfaces.Client) {
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

	// Debounce logic to prevent rapid reloads
	w.eventMutex.Lock()
	if lastTime, ok := w.eventTimes[event.Name]; ok && now.Sub(lastTime) < 500*time.Millisecond {
		log.Debugf("debouncing event for %s", event.Name)
		w.eventMutex.Unlock()
		return
	}
	w.eventTimes[event.Name] = now
	w.eventMutex.Unlock()

	// Handle config file changes
	if event.Name == w.configPath && (event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
		log.Infof("config file changed, reloading: %s", w.configPath)
		log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
		w.reloadConfig()
		return
	}

	// Handle auth directory changes incrementally
	if strings.HasPrefix(event.Name, w.authDir) && strings.HasSuffix(event.Name, ".json") {
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		if event.Op&fsnotify.Create == fsnotify.Create || event.Op&fsnotify.Write == fsnotify.Write {
			w.addOrUpdateClient(event.Name)
		} else if event.Op&fsnotify.Remove == fsnotify.Remove {
			w.removeClient(event.Name)
		}
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
		if len(oldConfig.CodexKey) != len(newConfig.CodexKey) {
			log.Debugf("  codex-api-key count: %d -> %d", len(oldConfig.CodexKey), len(newConfig.CodexKey))
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

// reloadClients performs a full scan of the auth directory and reloads all clients.
// This is used for initial startup and for handling config file reloads.
func (w *Watcher) reloadClients() {
	log.Debugf("starting full client reload process")

	w.clientsMutex.RLock()
	cfg := w.config
	oldClientCount := len(w.clients)
	w.clientsMutex.RUnlock()

	if cfg == nil {
		log.Error("config is nil, cannot reload clients")
		return
	}

	log.Debugf("scanning auth directory for initial load or full reload: %s", cfg.AuthDir)

	// Create new client map
	newClients := make(map[string]interfaces.Client)
	authFileCount := 0
	successfulAuthCount := 0

	// Handle tilde expansion for auth directory
	if strings.HasPrefix(cfg.AuthDir, "~") {
		home, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			log.Fatalf("failed to get home directory: %v", errUserHomeDir)
		}
		parts := strings.Split(cfg.AuthDir, string(os.PathSeparator))
		if len(parts) > 1 {
			parts[0] = home
			cfg.AuthDir = path.Join(parts...)
		} else {
			cfg.AuthDir = home
		}
	}

	// Load clients from auth directory
	errWalk := filepath.Walk(cfg.AuthDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			log.Debugf("error accessing path %s: %v", path, err)
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			authFileCount++
			log.Debugf("processing auth file %d: %s", authFileCount, filepath.Base(path))
			if cliClient, errCreateClientFromFile := w.createClientFromFile(path, cfg); errCreateClientFromFile == nil {
				newClients[path] = cliClient
				successfulAuthCount++
			} else {
				log.Errorf("failed to create client from file %s: %v", path, errCreateClientFromFile)
			}
		}
		return nil
	})
	if errWalk != nil {
		log.Errorf("error walking auth directory: %v", errWalk)
		return
	}
	log.Debugf("auth directory scan complete - found %d .json files, %d successful authentications", authFileCount, successfulAuthCount)

	// Note: API key-based clients are not stored in the map as they don't correspond to a file.
	// They are re-created each time, which is lightweight.
	clientSlice := w.clientsToSlice(newClients)

	// Add clients for Generative Language API keys if configured
	glAPIKeyCount := 0
	if len(cfg.GlAPIKey) > 0 {
		log.Debugf("processing %d Generative Language API Keys", len(cfg.GlAPIKey))
		for i := 0; i < len(cfg.GlAPIKey); i++ {
			httpClient := util.SetProxy(cfg, &http.Client{})
			log.Debugf("Initializing with Generative Language API Key %d...", i+1)
			cliClient := client.NewGeminiClient(httpClient, cfg, cfg.GlAPIKey[i])
			clientSlice = append(clientSlice, cliClient)
			glAPIKeyCount++
		}
		log.Debugf("Successfully initialized %d Generative Language API Key clients", glAPIKeyCount)
	}
	// ... (Claude, Codex, OpenAI-compat clients are handled similarly) ...
	claudeAPIKeyCount := 0
	if len(cfg.ClaudeKey) > 0 {
		log.Debugf("processing %d Claude API Keys", len(cfg.ClaudeKey))
		for i := 0; i < len(cfg.ClaudeKey); i++ {
			log.Debugf("Initializing with Claude API Key %d...", i+1)
			cliClient := client.NewClaudeClientWithKey(cfg, i)
			clientSlice = append(clientSlice, cliClient)
			claudeAPIKeyCount++
		}
		log.Debugf("Successfully initialized %d Claude API Key clients", claudeAPIKeyCount)
	}

	codexAPIKeyCount := 0
	if len(cfg.CodexKey) > 0 {
		log.Debugf("processing %d Codex API Keys", len(cfg.CodexKey))
		for i := 0; i < len(cfg.CodexKey); i++ {
			log.Debugf("Initializing with Codex API Key %d...", i+1)
			cliClient := client.NewCodexClientWithKey(cfg, i)
			clientSlice = append(clientSlice, cliClient)
			codexAPIKeyCount++
		}
		log.Debugf("Successfully initialized %d Codex API Key clients", codexAPIKeyCount)
	}

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
			clientSlice = append(clientSlice, compatClient)
			openAICompatCount++
		}
		log.Debugf("Successfully initialized %d OpenAI-compatibility clients", openAICompatCount)
	}

	// Unregister all old clients
	w.clientsMutex.RLock()
	for _, oldClient := range w.clients {
		if u, ok := any(oldClient).(interface{ UnregisterClient() }); ok {
			u.UnregisterClient()
		}
	}
	w.clientsMutex.RUnlock()

	// Update the client map
	w.clientsMutex.Lock()
	w.clients = newClients
	w.clientsMutex.Unlock()

	log.Infof("full client reload complete - old: %d clients, new: %d clients (%d auth files + %d GL API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		oldClientCount,
		len(clientSlice),
		successfulAuthCount,
		glAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		openAICompatCount,
	)

	// Trigger the callback to update the server with file-based + API key clients
	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback")
		combinedClients := w.buildCombinedClientMap(cfg)
		w.reloadCallback(combinedClients, cfg)
	}
}

// createClientFromFile creates a single client instance from a given token file path.
func (w *Watcher) createClientFromFile(path string, cfg *config.Config) (interfaces.Client, error) {
	data, errReadFile := os.ReadFile(path)
	if errReadFile != nil {
		return nil, errReadFile
	}

	// If the file is empty, it's likely an intermediate state (e.g., after touch, before write).
	// Silently ignore it and wait for a subsequent write event with content.
	if len(data) == 0 {
		return nil, nil // Not an error, just nothing to process yet.
	}

	tokenType := "gemini"
	typeResult := gjson.GetBytes(data, "type")
	if typeResult.Exists() {
		tokenType = typeResult.String()
	}

	var err error
	if tokenType == "gemini" {
		var ts gemini.GeminiTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			clientCtx := context.Background()
			geminiAuth := gemini.NewGeminiAuth()
			httpClient, errGetClient := geminiAuth.GetAuthenticatedClient(clientCtx, &ts, cfg)
			if errGetClient != nil {
				return nil, errGetClient
			}
			return client.NewGeminiCLIClient(httpClient, &ts, cfg), nil
		}
	} else if tokenType == "codex" {
		var ts codex.CodexTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			return client.NewCodexClient(cfg, &ts)
		}
	} else if tokenType == "claude" {
		var ts claude.ClaudeTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			return client.NewClaudeClient(cfg, &ts), nil
		}
	} else if tokenType == "qwen" {
		var ts qwen.QwenTokenStorage
		if err = json.Unmarshal(data, &ts); err == nil {
			return client.NewQwenClient(cfg, &ts), nil
		}
	}

	return nil, err
}

// clientsToSlice converts the client map to a slice.
func (w *Watcher) clientsToSlice(clientMap map[string]interfaces.Client) []interfaces.Client {
	s := make([]interfaces.Client, 0, len(clientMap))
	for _, v := range clientMap {
		s = append(s, v)
	}
	return s
}

// addOrUpdateClient handles the addition or update of a single client.
func (w *Watcher) addOrUpdateClient(path string) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()

	cfg := w.config
	if cfg == nil {
		log.Error("config is nil, cannot add or update client")
		return
	}

	// Unregister old client if it exists
	if oldClient, ok := w.clients[path]; ok {
		if u, canUnregister := any(oldClient).(interface{ UnregisterClient() }); canUnregister {
			log.Debugf("unregistering old client for updated file: %s", filepath.Base(path))
			u.UnregisterClient()
		}
	}

	newClient, err := w.createClientFromFile(path, cfg)
	if err != nil {
		log.Errorf("failed to create/update client for %s: %v", filepath.Base(path), err)
		// If creation fails, ensure the old client is removed from the map
		delete(w.clients, path)
	} else if newClient != nil { // Only update if a client was actually created
		log.Debugf("successfully created/updated client for %s", filepath.Base(path))
		w.clients[path] = newClient
	} else {
		// This case handles the empty file scenario gracefully
		log.Debugf("ignoring empty auth file: %s", filepath.Base(path))
		return // Do not trigger callback for an empty file
	}

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after add/update")
		combinedClients := w.buildCombinedClientMap(cfg)
		w.reloadCallback(combinedClients, cfg)
	}
}

// removeClient handles the removal of a single client.
func (w *Watcher) removeClient(path string) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()

	cfg := w.config

	// Unregister client if it exists
	if oldClient, ok := w.clients[path]; ok {
		if u, canUnregister := any(oldClient).(interface{ UnregisterClient() }); canUnregister {
			log.Debugf("unregistering client for removed file: %s", filepath.Base(path))
			u.UnregisterClient()
		}
		delete(w.clients, path)
		log.Debugf("removed client for %s", filepath.Base(path))

		if w.reloadCallback != nil {
			log.Debugf("triggering server update callback after removal")
			combinedClients := w.buildCombinedClientMap(cfg)
			w.reloadCallback(combinedClients, cfg)
		}
	}
}

// buildCombinedClientMap merges file-based clients with API key and compatibility clients.
// This ensures the callback receives the complete set of active clients.
func (w *Watcher) buildCombinedClientMap(cfg *config.Config) map[string]interfaces.Client {
	combined := make(map[string]interfaces.Client)

	// Include file-based clients
	for k, v := range w.clients {
		combined[k] = v
	}

	// Add Generative Language API Key clients
	if len(cfg.GlAPIKey) > 0 {
		for i := 0; i < len(cfg.GlAPIKey); i++ {
			httpClient := util.SetProxy(cfg, &http.Client{})
			cliClient := client.NewGeminiClient(httpClient, cfg, cfg.GlAPIKey[i])
			combined[fmt.Sprintf("apikey:gemini:%d", i)] = cliClient
		}
	}

	// Add Claude API Key clients
	if len(cfg.ClaudeKey) > 0 {
		for i := 0; i < len(cfg.ClaudeKey); i++ {
			cliClient := client.NewClaudeClientWithKey(cfg, i)
			combined[fmt.Sprintf("apikey:claude:%d", i)] = cliClient
		}
	}

	// Add Codex API Key clients
	if len(cfg.CodexKey) > 0 {
		for i := 0; i < len(cfg.CodexKey); i++ {
			cliClient := client.NewCodexClientWithKey(cfg, i)
			combined[fmt.Sprintf("apikey:codex:%d", i)] = cliClient
		}
	}

	// Add OpenAI compatibility clients
	if len(cfg.OpenAICompatibility) > 0 {
		for i := 0; i < len(cfg.OpenAICompatibility); i++ {
			compat := cfg.OpenAICompatibility[i]
			compatClient, errClient := client.NewOpenAICompatibilityClient(cfg, &compat)
			if errClient != nil {
				log.Errorf("failed to create OpenAI-compatibility client for %s: %v", compat.Name, errClient)
				continue
			}
			combined[fmt.Sprintf("openai-compat:%s:%d", compat.Name, i)] = compatClient
		}
	}

	return combined
}
