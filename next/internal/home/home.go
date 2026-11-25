// Package home finds pluxy's home: the one folder where a pluxy run by a person's user account
// keeps its files (config.json and, beside it, the credentials, packages, logs and state folders
// core makes). PLUXY_HOME names it; without it, it is a folder named pluxy in the user's
// application data folder: $XDG_DATA_HOME/pluxy (~/.local/share/pluxy) on Linux and other Unix
// systems, ~/Library/Application Support/pluxy on macOS and %LOCALAPPDATA%\pluxy on Windows.
package home

import (
	"fmt"
	"path/filepath"
	"runtime"
)

// Env is the variable that names pluxy's home in place of the default folder.
const Env = "PLUXY_HOME"

// ConfigName is the config file's name, in pluxy's home and in the current folder.
const ConfigName = "config.json"

// appID is the desktop app's identifier, the name of its data folders.
const appID = "dev.pluxy.desktop"

// Dir returns pluxy's home, reading the environment through getenv: PLUXY_HOME when it is set,
// else the default folder for this system. A PLUXY_HOME that is not an absolute path is an
// error, not a folder found from wherever pluxy happens to run.
func Dir(getenv func(string) string) (string, error) {
	return dir(runtime.GOOS, getenv)
}

// Config returns the config file in pluxy's home.
func Config(getenv func(string) string) (string, error) {
	d, err := Dir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ConfigName), nil
}

// AppCoreConfig returns where the desktop app keeps the config of the core it runs itself
// (core/config.json in the app's data folder, or on Windows its local data folder). That core
// is a separate pluxy, with its own keys and accounts.
func AppCoreConfig(getenv func(string) string) (string, error) {
	return appCoreConfig(runtime.GOOS, getenv)
}

func dir(goos string, getenv func(string) string) (string, error) {
	if d := getenv(Env); d != "" {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("%s=%q is not an absolute path", Env, d)
		}
		return filepath.Clean(d), nil
	}
	data, err := dataDir(goos, getenv)
	if err != nil {
		return "", fmt.Errorf("finding pluxy's home: %w; set %s to the folder to use", err, Env)
	}
	return filepath.Join(data, "pluxy"), nil
}

func appCoreConfig(goos string, getenv func(string) string) (string, error) {
	data, err := dataDir(goos, getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(data, appID, "core", ConfigName), nil
}

// dataDir is the user's application data folder: where the desktop app's data folder is too
// (Tauri's app data folder on Linux and macOS, its local one on Windows, where the app keeps
// every file that holds a key off the roaming profile).
func dataDir(goos string, getenv func(string) string) (string, error) {
	switch goos {
	case "windows":
		d := getenv("LOCALAPPDATA")
		if d == "" || !filepath.IsAbs(d) {
			return "", fmt.Errorf("%%LOCALAPPDATA%% is not set to an absolute path")
		}
		return d, nil
	case "darwin", "ios":
		h, err := homeDir(getenv)
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support"), nil
	default:
		// The XDG Base Directory Specification: a relative path in XDG_DATA_HOME is invalid and
		// is ignored.
		if d := getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
			return d, nil
		}
		h, err := homeDir(getenv)
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".local", "share"), nil
	}
}

func homeDir(getenv func(string) string) (string, error) {
	h := getenv("HOME")
	if h == "" || !filepath.IsAbs(h) {
		return "", fmt.Errorf("$HOME is not set to an absolute path")
	}
	return h, nil
}
