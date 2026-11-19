//go:build !windows

package private

import (
	"fmt"
	"os"
)

// openChecked opens the folder d.Path for Dir.Open. When create is set it first makes the
// folder, and any missing parents, readable only by its owner (0700); an existing folder keeps
// its mode. Files made in it are 0600 from the moment they exist (Create), so the folder's mode
// never lets another user read one.
func openChecked(d Dir, create bool) (*os.File, error) {
	dir := d.Path
	if st, err := os.Stat(dir); err == nil && !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	if create {
		if err := os.MkdirAll(dir, DirMode); err != nil {
			return nil, err
		}
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && !st.IsDir() {
		err = fmt.Errorf("%s is not a folder", dir)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
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

// Create creates the new file name in root readable only by its owner (0600), open for reading
// and writing. A name that exists is an error that os.IsExist accepts. name is a name in root
// itself, never a path. dir is root's path, for messages.
func Create(root *os.Root, dir, name string) (*os.File, error) {
	if err := checkName(dir, name); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, FileMode)
	if err != nil {
		return nil, err
	}
	// The umask may have taken bits off.
	if err := f.Chmod(FileMode); err != nil {
		f.Close()
		root.Remove(name)
		return nil, err
	}
	return f, nil
}

// fileOwner accepts every file on unix: each was made 0600 by its owner, and the folder check
// does not look at owners.
func fileOwner(f *os.File) (bool, string, error) { return true, "", nil }
