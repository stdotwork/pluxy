//go:build !windows

package private

import (
	"fmt"
	"os"
)

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

func fileOwner(f *os.File) (bool, string, error) { return true, "", nil }

func filePrivate(f *os.File) (bool, error) { return true, nil }

func copyAccess(src, dst string) error { return nil }
