package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestReloadConfigIfChanged_InvalidConfigKeepsPreviousConfig checks that a config file that
// fails to parse is not applied: no callback fires, the watcher keeps serving the previous
// config, and the bad bytes are not recorded as applied, so the corrected file still reloads.
func TestReloadConfigIfChanged_InvalidConfigKeepsPreviousConfig(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	writeConfig := func(content string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
	}
	validConfig := func(port string) string {
		return "port: " + port + "\nauth-dir: " + authDir + "\n"
	}

	var appliedPorts []int
	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: func(cfg *config.Config) { appliedPorts = append(appliedPorts, cfg.Port) },
	}
	currentPort := func() int {
		t.Helper()
		w.clientsMutex.RLock()
		defer w.clientsMutex.RUnlock()
		if w.config == nil {
			t.Fatalf("watcher has no config")
		}
		return w.config.Port
	}
	lastHash := func() string {
		w.clientsMutex.RLock()
		defer w.clientsMutex.RUnlock()
		return w.lastConfigHash
	}

	writeConfig(validConfig("8001"))
	w.reloadConfigIfChanged()
	if len(appliedPorts) != 1 || appliedPorts[0] != 8001 || currentPort() != 8001 {
		t.Fatalf("valid config: applied=%v current port=%d, want one reload with port 8001", appliedPorts, currentPort())
	}
	hashOfValid := lastHash()
	if hashOfValid == "" {
		t.Fatalf("valid reload did not record a config hash")
	}

	// A config that is not valid YAML for the schema must be rejected, on first sight and on retry.
	writeConfig("port: [this is not an int\n")
	for attempt := 1; attempt <= 2; attempt++ {
		w.reloadConfigIfChanged()
		if len(appliedPorts) != 1 {
			t.Fatalf("invalid config attempt %d fired the callback: applied=%v", attempt, appliedPorts)
		}
		if got := currentPort(); got != 8001 {
			t.Fatalf("invalid config attempt %d replaced the previous config: port=%d, want 8001", attempt, got)
		}
		if got := lastHash(); got != hashOfValid {
			t.Fatalf("invalid config attempt %d recorded its hash as applied: got %q, want %q", attempt, got, hashOfValid)
		}
	}

	// Fixing the file must reload with the new value.
	writeConfig(validConfig("8002"))
	w.reloadConfigIfChanged()
	if len(appliedPorts) != 2 || appliedPorts[1] != 8002 {
		t.Fatalf("after fixing the file: applied=%v, want [8001 8002]", appliedPorts)
	}
	if got := currentPort(); got != 8002 {
		t.Fatalf("after fixing the file: current port=%d, want 8002", got)
	}
}
