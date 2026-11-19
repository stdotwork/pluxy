//go:build !windows

package cred

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// openChecked opens the folder dir for open's check. When create is set it first makes dir, and
// any missing parents, readable only by its owner (0700), and sets that mode on an existing
// folder too. Loading changes nothing: each file is 0600 from the moment it is made, so the
// folder's mode never lets another user read a record.
func openChecked(dir string, create bool) (*os.File, error) {
	if st, err := os.Stat(dir); err == nil && !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	if create {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return nil, err
		}
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	st, err := d.Stat()
	if err == nil && !st.IsDir() {
		err = fmt.Errorf("%s is not a folder", dir)
	}
	// MkdirAll leaves an existing folder as it is and applies the umask to a new one.
	if err == nil && create {
		err = d.Chmod(dirMode)
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// sameDir reports whether a and b are the same folder.
func sameDir(a, b *os.File) (bool, error) {
	sa, err := a.Stat()
	if err != nil {
		return false, err
	}
	sb, err := b.Stat()
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

// createTemp creates a new temporary file in root readable only by its owner (0600), and returns
// it with its name in root. dir is root's path, for messages.
func createTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 100 {
		name, err := tempName()
		if err != nil {
			return nil, "", err
		}
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if err := f.Chmod(fileMode); err != nil {
			f.Close()
			root.Remove(name)
			return nil, "", err
		}
		return f, name, nil
	}
	return nil, "", &os.PathError{Op: "createtemp", Path: filepath.Join(dir, tmpPrefix+"*"), Err: fs.ErrExist}
}

// recordOwner accepts every record file on unix: each was made 0600 by its owner, and the
// folder check does not look at owners.
func recordOwner(f *os.File) (bool, string, error) { return true, "", nil }
