package util

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// SetLogLevel configures the logrus log level based on the configuration.
// It sets the log level to DebugLevel if debug mode is enabled, otherwise to InfoLevel.
func SetLogLevel(cfg *config.Config) {
	currentLevel := log.GetLevel()
	var newLevel log.Level
	if cfg.Debug {
		newLevel = log.DebugLevel
	} else {
		newLevel = log.InfoLevel
	}

	if currentLevel != newLevel {
		log.SetLevel(newLevel)
		log.Infof("log level changed from %s to %s (debug=%t)", currentLevel, newLevel, cfg.Debug)
	}
}

// CountAuthFiles returns the number of JSON auth files located under the provided directory.
// The function resolves leading tildes to the user's home directory and performs a case-insensitive
// match on the ".json" suffix so that files saved with uppercase extensions are also counted.
func CountAuthFiles(authDir string) int {
	if authDir == "" {
		return 0
	}
	if strings.HasPrefix(authDir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Debugf("countAuthFiles: failed to resolve home directory: %v", err)
			return 0
		}
		authDir = filepath.Join(home, authDir[1:])
	}
	count := 0
	walkErr := filepath.WalkDir(authDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Debugf("countAuthFiles: error accessing %s: %v", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			count++
		}
		return nil
	})
	if walkErr != nil {
		log.Debugf("countAuthFiles: walk error: %v", walkErr)
	}
	return count
}
