package misc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Separator used to visually group related log lines.
var credentialSeparator = strings.Repeat("-", 67)

// LogSavingCredentials emits a consistent log message when persisting auth material.
func LogSavingCredentials(path string) {
	if path == "" {
		return
	}
	// Use filepath.Clean so logs remain stable even if callers pass redundant separators.
	fmt.Printf("Saving credentials to %s\n", filepath.Clean(path))
}

// LogCredentialSeparator adds a visual separator to group auth/key processing logs.
func LogCredentialSeparator() {
	log.Debug(credentialSeparator)
}

// MergeMetadata serializes the source struct into a map and merges the provided metadata into it.
func MergeMetadata(source any, metadata map[string]any) (map[string]any, error) {
	var data map[string]any

	// Fast path: if source is already a map, just copy it to avoid mutation of original
	if srcMap, ok := source.(map[string]any); ok {
		data = make(map[string]any, len(srcMap)+len(metadata))
		for k, v := range srcMap {
			data[k] = v
		}
	} else if source != nil {
		// Slow path: marshal to JSON and back to map to respect JSON tags
		temp, errMarshal := json.Marshal(source)
		if errMarshal != nil {
			return nil, fmt.Errorf("failed to marshal source: %w", errMarshal)
		}
		if errUnmarshal := json.Unmarshal(temp, &data); errUnmarshal != nil {
			return nil, fmt.Errorf("failed to unmarshal to map: %w", errUnmarshal)
		}
	}

	// Merge extra metadata
	if metadata != nil {
		if data == nil {
			data = make(map[string]any)
		}
		for k, v := range metadata {
			data[k] = v
		}
	}

	return data, nil
}

// WriteFileAtomic writes data to path with mode 0600 by writing a temporary file in the same
// directory and renaming it over path, so a failed write never leaves a truncated credential file
// and an existing file's looser mode is not kept.
func WriteFileAtomic(path string, data []byte) error {
	tmpFile, errTmp := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if errTmp != nil {
		return fmt.Errorf("failed to create temp credential file: %w", errTmp)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if errChmod := tmpFile.Chmod(0o600); errChmod != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to chmod temp credential file: %w", errChmod)
	}
	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write token to temp file: %w", errWrite)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		log.Errorf("close temp credential file error: %v", errClose)
		return fmt.Errorf("failed to close temp credential file: %w", errClose)
	}
	if errRename := os.Rename(tmpPath, path); errRename != nil {
		return fmt.Errorf("failed to replace credential file: %w", errRename)
	}
	cleanupTmp = false
	return nil
}
