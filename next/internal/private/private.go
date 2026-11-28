package private

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DirMode  fs.FileMode = 0o700
	FileMode fs.FileMode = 0o600
)

type Dir struct {
	Path    string
	Setting string
	Redo    string
}

func (d Dir) Open(create bool) (*os.File, error) { return openChecked(d, create) }

func (d Dir) Root(checked *os.File) (*os.Root, error) {
	root, err := os.OpenRoot(d.Path)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	same, err := sameDir(checked, f)
	f.Close()
	if err == nil && !same {
		err = fmt.Errorf("%s was replaced while it was being opened; try again", d.Path)
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func (d Dir) OpenRoot(create bool) (*os.Root, error) {
	checked, err := d.Open(create)
	if err != nil {
		return nil, err
	}
	defer checked.Close()
	return d.Root(checked)
}

func TempName(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func CreateTemp(root *os.Root, dir, prefix string) (*os.File, string, error) {
	for range 100 {
		name, err := TempName(prefix)
		if err != nil {
			return nil, "", err
		}
		f, err := Create(root, dir, name)
		if err == nil {
			return f, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", &os.PathError{Op: "createtemp", Path: filepath.Join(dir, prefix+"*"), Err: fs.ErrExist}
}

func checkName(dir, name string) error {
	if !filepath.IsLocal(name) || strings.ContainsAny(name, `\/:`) {
		return &os.PathError{Op: "create", Path: filepath.Join(dir, name), Err: errors.New("not a name in the folder")}
	}
	return nil
}

func WriteFile(root *os.Root, dir, prefix, name string, data []byte) error {
	tmp, tmpName, err := CreateTemp(root, dir, prefix)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			root.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmpName, name); err != nil {
		return err
	}
	done = true
	return SyncDir(root)
}

// Windows cannot sync a folder; its renames are durable once they return.
func SyncDir(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func Owner(f *os.File) (ok bool, owner string, err error) { return fileOwner(f) }

// Making the folder private does not reach a file with a list of its own, such as one made before
// the folder was private, or one moved in from another folder on the same volume, and Windows
// lets every user reach a file in a folder they cannot open, so a caller rewrites such a file
// (WriteFile).
func FilePrivate(f *os.File) (bool, error) { return filePrivate(f) }

func CopyAccess(src, dst string) error { return copyAccess(src, dst) }
