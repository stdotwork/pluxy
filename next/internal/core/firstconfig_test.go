package core

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/server"
)

func TestWriteFirstConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config.json")
	fc, err := WriteFirstConfig(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fc.Path != path || !strings.HasPrefix(fc.Listen, "127.0.0.1:") || strings.HasSuffix(fc.Listen, ":0") || strings.HasSuffix(fc.Listen, ":8317") {
		t.Errorf("wrote %+v", fc)
	}
	cfg, err := server.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != fc.Listen || len(cfg.ClientKeys) != 1 || cfg.ManagementKey == "" || cfg.ManagementKey == cfg.ClientKeys[0] {
		t.Errorf("config: listen %s, client keys %d, management key set %v", cfg.Listen, len(cfg.ClientKeys), cfg.ManagementKey != "")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	before, _ := os.ReadFile(path)
	if _, err := WriteFirstConfig(path, 0); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second write: %v, want fs.ErrExist", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the second write changed the file")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want the config only", len(entries))
	}

	fc, err = WriteFirstConfig(filepath.Join(t.TempDir(), "config.json"), 18555)
	if err != nil || fc.Listen != "127.0.0.1:18555" {
		t.Errorf("port 18555: %+v, %v", fc, err)
	}
	if _, err := WriteFirstConfig(filepath.Join(t.TempDir(), "config.yaml"), 0); err == nil {
		t.Error("wrote a config.yaml")
	}
}

func TestPickPortFallsBackWhenTaken(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	taken := held.Addr().(*net.TCPAddr).Port
	got, err := pickPort(-1, taken)
	held.Close()
	if err != nil || got == taken || got <= 0 {
		t.Errorf("preferred port %d held: picked %d, %v; want another free port", taken, got, err)
	}
	free, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := pickPort(-1, free); err != nil || got != free {
		t.Errorf("preferred port %d free: picked %d, %v; want it", free, got, err)
	}
	if got, err := pickPort(18556, free); err != nil || got != 18556 {
		t.Errorf("a chosen port: picked %d, %v; want 18556", got, err)
	}
}
