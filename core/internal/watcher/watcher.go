// Package watcher provides file system monitoring functionality for the CLI Proxy API.
// It watches configuration files and authentication directories for changes,
// automatically reloading clients and configuration when files are modified.
// The package handles cross-platform file system events and supports hot-reloading.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	// "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
	// "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	// "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	// "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qwen"
	// "github.com/router-for-me/CLIProxyAPI/v6/internal/client"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	// "github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	// "github.com/tidwall/gjson"
)

// Watcher manages file watching for configuration and authentication files
type Watcher struct {
	configPath     string
	authDir        string
	config         *config.Config
	clientsMutex   sync.RWMutex
	reloadCallback func(*config.Config)
	watcher        *fsnotify.Watcher
	lastAuthHashes map[string]string
	lastConfigHash string
	authQueue      chan<- AuthUpdate
	currentAuths   map[string]*coreauth.Auth
	dispatchMu     sync.Mutex
	dispatchCond   *sync.Cond
	pendingUpdates map[string]AuthUpdate
	pendingOrder   []string
	dispatchCancel context.CancelFunc
}

// AuthUpdateAction represents the type of change detected in auth sources.
type AuthUpdateAction string

const (
	AuthUpdateActionAdd    AuthUpdateAction = "add"
	AuthUpdateActionModify AuthUpdateAction = "modify"
	AuthUpdateActionDelete AuthUpdateAction = "delete"
)

// AuthUpdate describes an incremental change to auth configuration.
type AuthUpdate struct {
	Action AuthUpdateAction
	ID     string
	Auth   *coreauth.Auth
}

const (
	// replaceCheckDelay is a short delay to allow atomic replace (rename) to settle
	// before deciding whether a Remove event indicates a real deletion.
	replaceCheckDelay = 50 * time.Millisecond
)

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func(*config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}

	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: reloadCallback,
		watcher:        watcher,
		lastAuthHashes: make(map[string]string),
	}
	w.dispatchCond = sync.NewCond(&w.dispatchMu)
	return w, nil
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

	// Perform an initial full reload based on current config and auth dir
	w.reloadClients()
	return nil
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	w.stopDispatch()
	return w.watcher.Close()
}

// SetConfig updates the current configuration
func (w *Watcher) SetConfig(cfg *config.Config) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cfg
}

// SetAuthUpdateQueue sets the queue used to emit auth updates.
func (w *Watcher) SetAuthUpdateQueue(queue chan<- AuthUpdate) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.authQueue = queue
	if w.dispatchCond == nil {
		w.dispatchCond = sync.NewCond(&w.dispatchMu)
	}
	if w.dispatchCancel != nil {
		w.dispatchCancel()
		if w.dispatchCond != nil {
			w.dispatchMu.Lock()
			w.dispatchCond.Broadcast()
			w.dispatchMu.Unlock()
		}
		w.dispatchCancel = nil
	}
	if queue != nil {
		ctx, cancel := context.WithCancel(context.Background())
		w.dispatchCancel = cancel
		go w.dispatchLoop(ctx)
	}
}

func (w *Watcher) refreshAuthState() {
	auths := w.SnapshotCoreAuths()
	w.clientsMutex.Lock()
	updates := w.prepareAuthUpdatesLocked(auths)
	w.clientsMutex.Unlock()
	w.dispatchAuthUpdates(updates)
}

func (w *Watcher) prepareAuthUpdatesLocked(auths []*coreauth.Auth) []AuthUpdate {
	newState := make(map[string]*coreauth.Auth, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.ID == "" {
			continue
		}
		newState[auth.ID] = auth.Clone()
	}
	if w.currentAuths == nil {
		w.currentAuths = newState
		if w.authQueue == nil {
			return nil
		}
		updates := make([]AuthUpdate, 0, len(newState))
		for id, auth := range newState {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: auth.Clone()})
		}
		return updates
	}
	if w.authQueue == nil {
		w.currentAuths = newState
		return nil
	}
	updates := make([]AuthUpdate, 0, len(newState)+len(w.currentAuths))
	for id, auth := range newState {
		if existing, ok := w.currentAuths[id]; !ok {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: auth.Clone()})
		} else if !authEqual(existing, auth) {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionModify, ID: id, Auth: auth.Clone()})
		}
	}
	for id := range w.currentAuths {
		if _, ok := newState[id]; !ok {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionDelete, ID: id})
		}
	}
	w.currentAuths = newState
	return updates
}

func (w *Watcher) dispatchAuthUpdates(updates []AuthUpdate) {
	if len(updates) == 0 {
		return
	}
	queue := w.getAuthQueue()
	if queue == nil {
		return
	}
	baseTS := time.Now().UnixNano()
	w.dispatchMu.Lock()
	if w.pendingUpdates == nil {
		w.pendingUpdates = make(map[string]AuthUpdate)
	}
	for idx, update := range updates {
		key := w.authUpdateKey(update, baseTS+int64(idx))
		if _, exists := w.pendingUpdates[key]; !exists {
			w.pendingOrder = append(w.pendingOrder, key)
		}
		w.pendingUpdates[key] = update
	}
	if w.dispatchCond != nil {
		w.dispatchCond.Signal()
	}
	w.dispatchMu.Unlock()
}

func (w *Watcher) authUpdateKey(update AuthUpdate, ts int64) string {
	if update.ID != "" {
		return update.ID
	}
	return fmt.Sprintf("%s:%d", update.Action, ts)
}

func (w *Watcher) dispatchLoop(ctx context.Context) {
	for {
		batch, ok := w.nextPendingBatch(ctx)
		if !ok {
			return
		}
		queue := w.getAuthQueue()
		if queue == nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, update := range batch {
			select {
			case queue <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (w *Watcher) nextPendingBatch(ctx context.Context) ([]AuthUpdate, bool) {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	for len(w.pendingOrder) == 0 {
		if ctx.Err() != nil {
			return nil, false
		}
		w.dispatchCond.Wait()
		if ctx.Err() != nil {
			return nil, false
		}
	}
	batch := make([]AuthUpdate, 0, len(w.pendingOrder))
	for _, key := range w.pendingOrder {
		batch = append(batch, w.pendingUpdates[key])
		delete(w.pendingUpdates, key)
	}
	w.pendingOrder = w.pendingOrder[:0]
	return batch, true
}

func (w *Watcher) getAuthQueue() chan<- AuthUpdate {
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	return w.authQueue
}

func (w *Watcher) stopDispatch() {
	if w.dispatchCancel != nil {
		w.dispatchCancel()
		w.dispatchCancel = nil
	}
	w.dispatchMu.Lock()
	w.pendingOrder = nil
	w.pendingUpdates = nil
	if w.dispatchCond != nil {
		w.dispatchCond.Broadcast()
	}
	w.dispatchMu.Unlock()
	w.clientsMutex.Lock()
	w.authQueue = nil
	w.clientsMutex.Unlock()
}

func authEqual(a, b *coreauth.Auth) bool {
	return reflect.DeepEqual(normalizeAuth(a), normalizeAuth(b))
}

func normalizeAuth(a *coreauth.Auth) *coreauth.Auth {
	if a == nil {
		return nil
	}
	clone := a.Clone()
	clone.CreatedAt = time.Time{}
	clone.UpdatedAt = time.Time{}
	clone.LastRefreshedAt = time.Time{}
	clone.NextRefreshAfter = time.Time{}
	clone.Runtime = nil
	clone.Quota.NextRecoverAt = time.Time{}
	return clone
}

// computeOpenAICompatModelsHash returns a stable hash for the compatibility models so that
// changes to the model list trigger auth updates during hot reload.
func computeOpenAICompatModelsHash(models []config.OpenAICompatibilityModel) string {
	if len(models) == 0 {
		return ""
	}
	data, err := json.Marshal(models)
	if err != nil || len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SetClients sets the file-based clients.
// SetClients removed
// SetAPIKeyClients removed

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
	// Filter only relevant events: config file or auth-dir JSON files.
	isConfigEvent := event.Name == w.configPath && (event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create)
	isAuthJSON := strings.HasPrefix(event.Name, w.authDir) && strings.HasSuffix(event.Name, ".json")
	if !isConfigEvent && !isAuthJSON {
		// Ignore unrelated files (e.g., cookie snapshots *.cookie) and other noise.
		return
	}

	now := time.Now()
	log.Debugf("file system event detected: %s %s", event.Op.String(), event.Name)

	// Handle config file changes
	if isConfigEvent {
		log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
		data, err := os.ReadFile(w.configPath)
		if err != nil {
			log.Errorf("failed to read config file for hash check: %v", err)
			return
		}
		if len(data) == 0 {
			log.Debugf("ignoring empty config file write event")
			return
		}
		sum := sha256.Sum256(data)
		newHash := hex.EncodeToString(sum[:])

		w.clientsMutex.RLock()
		currentHash := w.lastConfigHash
		w.clientsMutex.RUnlock()

		if currentHash != "" && currentHash == newHash {
			log.Debugf("config file content unchanged (hash match), skipping reload")
			return
		}
		fmt.Printf("config file changed, reloading: %s\n", w.configPath)
		if w.reloadConfig() {
			w.clientsMutex.Lock()
			w.lastConfigHash = newHash
			w.clientsMutex.Unlock()
		}
		return
	}

	// Handle auth directory changes incrementally (.json only)
	fmt.Printf("auth file changed (%s): %s, processing incrementally\n", event.Op.String(), filepath.Base(event.Name))
	if event.Op&fsnotify.Create == fsnotify.Create || event.Op&fsnotify.Write == fsnotify.Write {
		w.addOrUpdateClient(event.Name)
	} else if event.Op&fsnotify.Remove == fsnotify.Remove {
		// Atomic replace on some platforms may surface as Remove+Create for the target path.
		// Wait briefly; if the file exists again, treat as update instead of removal.
		time.Sleep(replaceCheckDelay)
		if _, statErr := os.Stat(event.Name); statErr == nil {
			// File exists after a short delay; handle as an update.
			w.addOrUpdateClient(event.Name)
			return
		}
		w.removeClient(event.Name)
	}
}

// reloadConfig reloads the configuration and triggers a full reload
func (w *Watcher) reloadConfig() bool {
	log.Debugf("starting config reload from: %s", w.configPath)

	newConfig, errLoadConfig := config.LoadConfig(w.configPath)
	if errLoadConfig != nil {
		log.Errorf("failed to reload config: %v", errLoadConfig)
		return false
	}

	w.clientsMutex.Lock()
	oldConfig := w.config
	w.config = newConfig
	w.clientsMutex.Unlock()

	// Always apply the current log level based on the latest config.
	// This ensures logrus reflects the desired level even if change detection misses.
	util.SetLogLevel(newConfig)
	// Additional debug for visibility when the flag actually changes.
	if oldConfig != nil && oldConfig.Debug != newConfig.Debug {
		log.Debugf("log level updated - debug mode changed from %t to %t", oldConfig.Debug, newConfig.Debug)
	}

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
		if oldConfig.GeminiWeb.Context != newConfig.GeminiWeb.Context {
			log.Debugf("  gemini-web.context: %t -> %t", oldConfig.GeminiWeb.Context, newConfig.GeminiWeb.Context)
		}
		if oldConfig.GeminiWeb.MaxCharsPerRequest != newConfig.GeminiWeb.MaxCharsPerRequest {
			log.Debugf("  gemini-web.max-chars-per-request: %d -> %d", oldConfig.GeminiWeb.MaxCharsPerRequest, newConfig.GeminiWeb.MaxCharsPerRequest)
		}
		if oldConfig.GeminiWeb.DisableContinuationHint != newConfig.GeminiWeb.DisableContinuationHint {
			log.Debugf("  gemini-web.disable-continuation-hint: %t -> %t", oldConfig.GeminiWeb.DisableContinuationHint, newConfig.GeminiWeb.DisableContinuationHint)
		}
		if oldConfig.GeminiWeb.CodeMode != newConfig.GeminiWeb.CodeMode {
			log.Debugf("  gemini-web.code-mode: %t -> %t", oldConfig.GeminiWeb.CodeMode, newConfig.GeminiWeb.CodeMode)
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
		if oldConfig.RemoteManagement.AllowRemote != newConfig.RemoteManagement.AllowRemote {
			log.Debugf("  remote-management.allow-remote: %t -> %t", oldConfig.RemoteManagement.AllowRemote, newConfig.RemoteManagement.AllowRemote)
		}
		if oldConfig.LoggingToFile != newConfig.LoggingToFile {
			log.Debugf("  logging-to-file: %t -> %t", oldConfig.LoggingToFile, newConfig.LoggingToFile)
		}
		if oldConfig.UsageStatisticsEnabled != newConfig.UsageStatisticsEnabled {
			log.Debugf("  usage-statistics-enabled: %t -> %t", oldConfig.UsageStatisticsEnabled, newConfig.UsageStatisticsEnabled)
		}
	}

	log.Infof("config successfully reloaded, triggering client reload")
	// Reload clients with new config
	w.reloadClients()
	return true
}

// reloadClients performs a full scan and reload of all clients.
func (w *Watcher) reloadClients() {
	log.Debugf("starting full client reload process")

	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()

	if cfg == nil {
		log.Error("config is nil, cannot reload clients")
		return
	}

	// Unregister all old API key clients before creating new ones
	// no legacy clients to unregister

	// Create new API key clients based on the new config
	glAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount := BuildAPIKeyClients(cfg)
	log.Debugf("created %d new API key clients", 0)

	// Load file-based clients
	authFileCount := w.loadFileClients(cfg)
	log.Debugf("loaded %d new file-based clients", 0)

	// no legacy file-based clients to unregister

	// Update client maps
	w.clientsMutex.Lock()

	// Rebuild auth file hash cache for current clients
	w.lastAuthHashes = make(map[string]string)
	// Recompute hashes for current auth files
	_ = filepath.Walk(cfg.AuthDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
			if data, errReadFile := os.ReadFile(path); errReadFile == nil && len(data) > 0 {
				sum := sha256.Sum256(data)
				w.lastAuthHashes[path] = hex.EncodeToString(sum[:])
			}
		}
		return nil
	})
	w.clientsMutex.Unlock()

	totalNewClients := authFileCount + glAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount

	// Ensure consumers observe the new configuration before auth updates dispatch.
	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback before auth refresh")
		w.reloadCallback(cfg)
	}

	w.refreshAuthState()

	log.Infof("full client reload complete - old: %d clients, new: %d clients (%d auth files + %d GL API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		0,
		totalNewClients,
		authFileCount,
		glAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		openAICompatCount,
	)
}

// createClientFromFile creates a single client instance from a given token file path.
// createClientFromFile removed (legacy)

// addOrUpdateClient handles the addition or update of a single client.
func (w *Watcher) addOrUpdateClient(path string) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		log.Errorf("failed to read auth file %s: %v", filepath.Base(path), errRead)
		return
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty auth file: %s", filepath.Base(path))
		return
	}

	sum := sha256.Sum256(data)
	curHash := hex.EncodeToString(sum[:])

	w.clientsMutex.Lock()

	cfg := w.config
	if cfg == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}
	if prev, ok := w.lastAuthHashes[path]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Update hash cache
	w.lastAuthHashes[path] = curHash

	w.clientsMutex.Unlock() // Unlock before the callback

	w.refreshAuthState()

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after add/update")
		w.reloadCallback(cfg)
	}
}

// removeClient handles the removal of a single client.
func (w *Watcher) removeClient(path string) {
	w.clientsMutex.Lock()

	cfg := w.config
	delete(w.lastAuthHashes, path)

	w.clientsMutex.Unlock() // Release the lock before the callback

	w.refreshAuthState()

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after removal")
		w.reloadCallback(cfg)
	}
}

// SnapshotCombinedClients returns a snapshot of current combined clients.
// SnapshotCombinedClients removed

// SnapshotCoreAuths converts current clients snapshot into core auth entries.
func (w *Watcher) SnapshotCoreAuths() []*coreauth.Auth {
	out := make([]*coreauth.Auth, 0, 32)
	now := time.Now()
	// Also synthesize auth entries for OpenAI-compatibility providers directly from config
	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()
	if cfg != nil {
		// Gemini official API keys -> synthesize auths
		for i := range cfg.GlAPIKey {
			k := cfg.GlAPIKey[i]
			a := &coreauth.Auth{
				ID:       fmt.Sprintf("gemini:apikey:%d", i),
				Provider: "gemini",
				Label:    "gemini-apikey",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"source":  fmt.Sprintf("config:gemini#%d", i),
					"api_key": k,
				},
				CreatedAt: now,
				UpdatedAt: now,
			}
			out = append(out, a)
		}
		// Claude API keys -> synthesize auths
		for i := range cfg.ClaudeKey {
			ck := cfg.ClaudeKey[i]
			attrs := map[string]string{
				"source":  fmt.Sprintf("config:claude#%d", i),
				"api_key": ck.APIKey,
			}
			if ck.BaseURL != "" {
				attrs["base_url"] = ck.BaseURL
			}
			a := &coreauth.Auth{
				ID:         fmt.Sprintf("claude:apikey:%d", i),
				Provider:   "claude",
				Label:      "claude-apikey",
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			out = append(out, a)
		}
		// Codex API keys -> synthesize auths
		for i := range cfg.CodexKey {
			ck := cfg.CodexKey[i]
			attrs := map[string]string{
				"source":  fmt.Sprintf("config:codex#%d", i),
				"api_key": ck.APIKey,
			}
			if ck.BaseURL != "" {
				attrs["base_url"] = ck.BaseURL
			}
			a := &coreauth.Auth{
				ID:         fmt.Sprintf("codex:apikey:%d", i),
				Provider:   "codex",
				Label:      "codex-apikey",
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			out = append(out, a)
		}
		for i := range cfg.OpenAICompatibility {
			compat := &cfg.OpenAICompatibility[i]
			providerName := strings.ToLower(strings.TrimSpace(compat.Name))
			if providerName == "" {
				providerName = "openai-compatibility"
			}
			base := compat.BaseURL
			for j := range compat.APIKeys {
				key := compat.APIKeys[j]
				attrs := map[string]string{
					"source":       fmt.Sprintf("config:%s#%d", compat.Name, j),
					"base_url":     base,
					"api_key":      key,
					"compat_name":  compat.Name,
					"provider_key": providerName,
				}
				if hash := computeOpenAICompatModelsHash(compat.Models); hash != "" {
					attrs["models_hash"] = hash
				}
				a := &coreauth.Auth{
					ID:         fmt.Sprintf("openai-compatibility:%s:%d", compat.Name, j),
					Provider:   providerName,
					Label:      compat.Name,
					Status:     coreauth.StatusActive,
					Attributes: attrs,
					CreatedAt:  now,
					UpdatedAt:  now,
				}
				out = append(out, a)
			}
		}
	}
	// Also synthesize auth entries directly from auth files (for OAuth/file-backed providers)
	entries, _ := os.ReadDir(w.authDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(w.authDir, name)
		data, err := os.ReadFile(full)
		if err != nil || len(data) == 0 {
			continue
		}
		var metadata map[string]any
		if err = json.Unmarshal(data, &metadata); err != nil {
			continue
		}
		t, _ := metadata["type"].(string)
		if t == "" {
			continue
		}
		provider := strings.ToLower(t)
		if provider == "gemini" {
			provider = "gemini-cli"
		}
		label := provider
		if email, _ := metadata["email"].(string); email != "" {
			label = email
		}
		// Use relative path under authDir as ID to stay consistent with the file-based token store
		id := full
		if rel, errRel := filepath.Rel(w.authDir, full); errRel == nil && rel != "" {
			id = rel
		}

		a := &coreauth.Auth{
			ID:       id,
			Provider: provider,
			Label:    label,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"source": full,
				"path":   full,
			},
			Metadata:  metadata,
			CreatedAt: now,
			UpdatedAt: now,
		}
		out = append(out, a)
	}
	return out
}

// buildCombinedClientMap merges file-based clients with API key clients from the cache.
// buildCombinedClientMap removed

// unregisterClientWithReason attempts to call client-specific unregister hooks with context.
// unregisterClientWithReason removed

// loadFileClients scans the auth directory and creates clients from .json files.
func (w *Watcher) loadFileClients(cfg *config.Config) int {
	authFileCount := 0
	successfulAuthCount := 0

	authDir := cfg.AuthDir
	if strings.HasPrefix(authDir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("failed to get home directory: %v", err)
			return 0
		}
		authDir = filepath.Join(home, authDir[1:])
	}

	errWalk := filepath.Walk(authDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			log.Debugf("error accessing path %s: %v", path, err)
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
			authFileCount++
			log.Debugf("processing auth file %d: %s", authFileCount, filepath.Base(path))
			// Count readable JSON files as successful auth entries
			if data, errCreate := os.ReadFile(path); errCreate == nil && len(data) > 0 {
				successfulAuthCount++
			}
		}
		return nil
	})

	if errWalk != nil {
		log.Errorf("error walking auth directory: %v", errWalk)
	}
	log.Debugf("auth directory scan complete - found %d .json files, %d readable", authFileCount, successfulAuthCount)
	return authFileCount
}

func BuildAPIKeyClients(cfg *config.Config) (int, int, int, int) {
	glAPIKeyCount := 0
	claudeAPIKeyCount := 0
	codexAPIKeyCount := 0
	openAICompatCount := 0

	if len(cfg.GlAPIKey) > 0 {
		// Stateless executor handles Gemini API keys; avoid constructing legacy clients.
		glAPIKeyCount += len(cfg.GlAPIKey)
	}
	if len(cfg.ClaudeKey) > 0 {
		claudeAPIKeyCount += len(cfg.ClaudeKey)
	}
	if len(cfg.CodexKey) > 0 {
		codexAPIKeyCount += len(cfg.CodexKey)
	}
	if len(cfg.OpenAICompatibility) > 0 {
		// Do not construct legacy clients for OpenAI-compat providers; these are handled by the stateless executor.
		for _, compatConfig := range cfg.OpenAICompatibility {
			openAICompatCount += len(compatConfig.APIKeys)
		}
	}
	return glAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount
}
