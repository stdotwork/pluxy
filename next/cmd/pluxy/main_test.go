package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives the tests a home folder of their own: a core they start scans the machine's
// home, config and cache folders for client tools.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "pluxy-cli-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	xdg := filepath.Join(home, ".xdg")
	for k, v := range map[string]string{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(xdg, "config"),
		"XDG_DATA_HOME":   filepath.Join(xdg, "data"),
		"XDG_STATE_HOME":  filepath.Join(xdg, "state"),
		"XDG_CACHE_HOME":  filepath.Join(xdg, "cache"),
		"CODEX_HOME":      filepath.Join(home, ".codex"),
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
