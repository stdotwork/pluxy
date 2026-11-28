package home

import (
	"fmt"
	"path/filepath"
	"runtime"
)

const Env = "PLUXY_HOME"

const ConfigName = "config.json"

const appID = "dev.pluxy.desktop"

func Dir(getenv func(string) string) (string, error) {
	return dir(runtime.GOOS, getenv)
}

func Config(getenv func(string) string) (string, error) {
	d, err := Dir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ConfigName), nil
}

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
