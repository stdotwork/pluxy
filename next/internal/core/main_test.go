package core

import (
	"fmt"
	"os"
	"testing"
)

// TestMain gives the tests a home folder of their own: a core they start scans the machine's
// home, config and cache folders for client tools.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "pluxy-core-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range homeEnv(home) {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
