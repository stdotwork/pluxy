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

// DefaultPort is the port a first config listens on when it is free: the port clients of
// today's core already know.
const DefaultPort = 8317

// FirstConfig is what WriteFirstConfig wrote.
type FirstConfig struct {
	// Path is the config file's absolute path.
	Path string
	// Listen is the address it listens on.
	Listen string
}

// WriteFirstConfig writes a new config file at path for a core that has none: it listens on
// 127.0.0.1, with one new client key and a new management key, and only this user can read
// it. Its folder is made when it is missing, and refused when it is a link, belongs to another
// user, or on unix its mode lets group or others write in it (home.OpenDir: on Windows such a
// folder is made private instead; the error is then a *home.Refusal). Only that folder is
// checked, not those above it, and on macOS by its mode bits, not its access list. Only this
// first write checks it: a config already there is used without a check. port is the port to
// listen on; with port -1 it is DefaultPort when that is free and a free one when it is taken,
// and with port 0 a free one. It never replaces a file at path (the error is then fs.ErrExist), and writes JSON
// only: a config.yaml is today's core's, written by a person.
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

// pickPort returns the port a first config listens on: port when it is above 0; with port -1,
// preferred when it is free now and a free port when it is taken; with port 0, a free port.
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

// portFree reports whether port can be listened on at 127.0.0.1 now.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// freePort returns a port at 127.0.0.1 that no program listens on now.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// ConfigError is Start's error when the config file could not be read or was refused: safe
// mode does not get past it.
type ConfigError struct{ Err error }

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }
