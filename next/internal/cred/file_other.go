//go:build !windows

package cred

import "os"

// makeDir makes dir, and any missing parents, readable only by its owner (0700).
func makeDir(dir string) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	// MkdirAll leaves an existing folder as it is and applies the umask to a new one.
	return os.Chmod(dir, dirMode)
}

// keepPrivate does nothing on unix: each file is 0600 from the moment it is made, so the
// folder's mode never lets another user read a record, and a save sets the folder's mode.
func keepPrivate(dir string) error { return nil }

// createTemp creates a new temporary file in dir readable only by its owner (0600).
func createTemp(dir string) (*os.File, error) {
	f, err := os.CreateTemp(dir, tmpGlob) // created 0600
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}
