package core

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"path/filepath"
	"strconv"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/home"
	"github.com/stdotwork/pluxy/next/internal/server"
)

const DefaultPort = 8317

type FirstConfig struct {
	Path   string
	Listen string
}

func WriteFirstConfig(path string, port int) (FirstConfig, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return FirstConfig{}, err
	}
	if server.IsYAML(abs) {
		return FirstConfig{}, fmt.Errorf("%s is a config.yaml, which pluxy does not write: name a .json file", abs)
	}
	if port < -1 || port > 65535 {
		return FirstConfig{}, fmt.Errorf("port %d is not a port", port)
	}
	if port, err = pickPort(port, DefaultPort); err != nil {
		return FirstConfig{}, fmt.Errorf("finding a free port: %w", err)
	}
	listen := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	b, err := json.MarshalIndent(struct {
		Listen        string   `json:"listen"`
		ClientKeys    []string `json:"client_keys"`
		ManagementKey string   `json:"management_key"`
	}{listen, []string{rand.Text()}, rand.Text()}, "", "  ")
	if err != nil {
		return FirstConfig{}, err
	}
	dir := filepath.Dir(abs)
	root, err := home.OpenDir(dir)
	if err != nil {
		return FirstConfig{}, err
	}
	defer root.Close()
	if err := cred.CreatePrivateIn(root, dir, filepath.Base(abs), append(b, '\n')); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return FirstConfig{}, &fs.PathError{Op: "write a first config", Path: abs, Err: fs.ErrExist}
		}
		return FirstConfig{}, fmt.Errorf("writing %s: %w", abs, err)
	}
	return FirstConfig{Path: abs, Listen: listen}, nil
}

func pickPort(port, preferred int) (int, error) {
	if port == -1 {
		if portFree(preferred) {
			return preferred, nil
		}
		port = 0
	}
	if port == 0 {
		return freePort()
	}
	return port, nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

type ConfigError struct{ Err error }

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }
