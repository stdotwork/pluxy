// Package private keeps core's private folders and the files in them: the credential store, the
// extensions' settings and the logs. What it makes can be read only by core's user (and, on
// Windows, SYSTEM): on unix by its modes (folders 0700, files 0600); on Windows, where Go
// applies only the read-only bit of a mode, by a protected access list (private_windows.go).
//
// A folder is checked through a handle to it (Dir.Open) and then opened as an os.Root that must
// be the folder the check judged (Dir.Root), so a folder renamed into its place afterwards is
// never used. Files are made in the root, private from the moment they exist (Create,
// CreateTemp, WriteFile).
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
	// DirMode is a private folder's mode on unix.
	DirMode fs.FileMode = 0o700
	// FileMode is a private file's mode on unix.
	FileMode fs.FileMode = 0o600
)

// Dir is a private folder, and what its refusals say.
type Dir struct {
	// Path is the folder.
	Path string
	// Setting is the config setting that names the folder, for the refusal of a link there;
	// empty when none does.
	Setting string
	// Redo completes "move the folder away and ..." in the refusal of a folder another account
	// owns: what has to be done again in a new folder. Empty when core makes it all again.
	Redo string
}

// Open opens the folder itself and checks it, after making it, and any missing parents, when
// create is set. On unix a new folder is 0700 and an existing one is left as it is. On Windows a
// new folder gets the private access list as it is made; a link, a file or a folder another
// account owns is refused; and an existing folder whose list is not private gets the private
// list (openChecked in private_windows.go). The returned handle is for Root.
func (d Dir) Open(create bool) (*os.File, error) { return openChecked(d, create) }

// Root opens the folder as a root and checks that it is the folder checked, from Open, is open
// to: one swapped in between is refused.
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

// OpenRoot opens and checks the folder (Open) and opens it as a root (Root).
func (d Dir) OpenRoot(create bool) (*os.Root, error) {
	checked, err := d.Open(create)
	if err != nil {
		return nil, err
	}
	defer checked.Close()
	return d.Root(checked)
}

// TempName returns a new name for a temporary file: prefix and 16 random hex digits.
func TempName(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// CreateTemp creates a new private file in root, named by TempName(prefix), and returns it, open
// for reading and writing, with its name in root. dir is root's path, for messages.
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

// checkName refuses a name for Create that is not a name in the folder itself.
func checkName(dir, name string) error {
	if !filepath.IsLocal(name) || strings.ContainsAny(name, `\/:`) {
		return &os.PathError{Op: "create", Path: filepath.Join(dir, name), Err: errors.New("not a name in the folder")}
	}
	return nil
}

// WriteFile writes data to name in root through a private temporary file (CreateTemp with
// prefix), synced and renamed over the old file, so a crash leaves the old file or the new one,
// and the new one is private. dir is root's path, for messages.
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

// SyncDir makes a rename or removal in root durable. Windows cannot sync a folder; its renames
// are durable once they return.
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

// Owner reports whether the owner of f, a file in a private folder, is one the folder's check
// accepts, and names it. On unix every file is accepted: the folder check there does not look at
// owners. On Windows the owner must be this user, the token's default owner or SYSTEM; another
// account may have put the file there before the folder was private.
func Owner(f *os.File) (ok bool, owner string, err error) { return fileOwner(f) }
