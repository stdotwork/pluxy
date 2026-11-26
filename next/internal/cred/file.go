package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/private"
)

// FileBackend keeps each record in its own JSON file in one folder: the minimal portable
// backend. Each file can be read only by core's user (and, on Windows, SYSTEM), and the folder is
// made private on each save: on unix by their modes (0700 and 0600); on Windows, where Go applies
// only the read-only bit, by a protected access list (internal/private), and there the folder is
// also checked and made private when the records are loaded, before any is read, and a record
// whose own list is not private is then written again as a private file. A save writes a
// temporary file in the same folder, syncs it and renames it over the old one, so a crash leaves
// the old record or the new one.
//
// Every read, write and rename goes through an os.Root opened on the folder the check judged, so
// a folder renamed into its place afterwards is never used.
//
// shortcut: no file lock, so two cores must not share a folder (their refreshes would not be
// serialised against each other); add a lock or a lease when one folder serves several cores.
type FileBackend struct {
	Dir string
}

const (
	ext       = ".json"
	tmpPrefix = ".tmp-"
)

// beforeRoot runs between the folder's check and its opening as a root. Tests swap the folder
// there.
var beforeRoot = func(dir string) {}

// open checks the folder (private.Dir.Open, which makes it first when create is set) and opens
// it as a root. The root must be the folder the check judged: one swapped in between is refused.
func (f FileBackend) open(create bool) (*os.Root, error) {
	dir := private.Dir{Path: f.Dir, Setting: "credentials_dir", Redo: "sign the accounts in again"}
	checked, err := dir.Open(create)
	if err != nil {
		return nil, err
	}
	defer checked.Close()
	// On unix a save also sets the folder's mode, which mkdir left to the umask or which an
	// existing folder had. Loading changes nothing: each file is 0600 from the moment it is made.
	if create && runtime.GOOS != "windows" {
		if err := checked.Chmod(private.DirMode); err != nil {
			return nil, err
		}
	}
	beforeRoot(f.Dir)
	return dir.Root(checked)
}

// storeError gives an error of the file backend the credential store's prefix.
func storeError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("credential store: %w", err)
}

// Load reads every record in the folder. A missing folder holds no records. A record file that
// does not read as a record is skipped with a notice in the log and returned as Broken, so one
// bad file does not keep the other accounts from being read.
func (f FileBackend) Load() ([]Record, []Broken, error) {
	recs, broken, err := f.load()
	return recs, broken, storeError(err)
}

func (f FileBackend) load() ([]Record, []Broken, error) {
	// A record that is never renewed is never saved again, so the folder is checked (and on
	// Windows made private) here too, not only by the next save.
	root, err := f.open(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		return nil, nil, err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil, nil, err
	}
	var out []Record
	var broken []Broken
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, tmpPrefix) {
			// Left by a save that crashed before its rename; the record it was replacing is
			// still whole.
			_ = root.Remove(name)
			continue
		}
		if e.IsDir() || !strings.HasSuffix(name, ext) {
			continue
		}
		b, err := f.readRecord(root, name)
		if err != nil {
			return nil, nil, err
		}
		if b == nil {
			continue
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			broken = append(broken, f.broken(name, fmt.Errorf("%s: %w", name, err)))
			continue
		}
		if r.ID+ext != name {
			broken = append(broken, f.broken(name, fmt.Errorf("%s holds account %q", name, r.ID)))
			continue
		}
		out = append(out, r)
	}
	return out, broken, nil
}

// broken makes the Broken entry for file name, which could not be read as a record, and says so
// in the log as readRecord does for the files it skips: one bad file must not keep core from
// reading the others.
func (f FileBackend) broken(name string, err error) Broken {
	path := filepath.Join(f.Dir, name)
	log.Printf("credential store: skipped %s: %v", path, err)
	return Broken{ID: strings.TrimSuffix(name, ext), Path: path, Err: err}
}

// readRecord reads the record file name in root. A file that is not a regular file (a link, say),
// or one owned by an account the folder's check would not accept (private.Owner), is skipped with a
// notice in the log, and nil is returned: core did not make it, and another account may have put
// it there before the folder was private. A record whose own access list is not private
// (private.FilePrivate: on Windows, one made before the folder was private or moved in from
// another folder) is written again as a private file, with a notice in the log, before it is
// returned.
func (f FileBackend) readRecord(root *os.Root, name string) ([]byte, error) {
	path := filepath.Join(f.Dir, name)
	skip := func(why string) ([]byte, error) {
		log.Printf("credential store: skipped %s: %s", path, why)
		return nil, nil
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return skip("it is not a regular file (a link?); core reads only the record files it wrote")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if st, err := file.Stat(); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return skip("it is not a regular file (a link?); core reads only the record files it wrote")
	}
	if ok, owner, err := private.Owner(file); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	} else if !ok {
		return skip("it is owned by " + owner + ", not by this user. Take ownership of it from an elevated prompt " +
			"(for example: takeown /f \"" + path + "\"), or remove it and sign the account in again")
	}
	ok, err := private.FilePrivate(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	b, err := io.ReadAll(file)
	if err != nil || ok {
		return b, err
	}
	// Closed first: Windows does not replace a file that is open.
	file.Close()
	if err := private.WriteFile(root, f.Dir, tmpPrefix, name, b); err != nil {
		return nil, fmt.Errorf("making %s private: %w", name, err)
	}
	log.Printf("credential store: made %s private: its own access list let in more than this user and SYSTEM", path)
	return b, nil
}

// Save writes r atomically.
func (f FileBackend) Save(r Record) error {
	return storeError(f.save(r))
}

func (f FileBackend) save(r Record) error {
	if !ValidID(r.ID) {
		return fmt.Errorf("account ID %q is not valid", r.ID)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	root, err := f.open(true)
	if err != nil {
		return err
	}
	defer root.Close()
	return private.WriteFile(root, f.Dir, tmpPrefix, r.ID+ext, b)
}

// Delete removes the record for id.
func (f FileBackend) Delete(id string) error {
	return storeError(f.delete(id))
}

func (f FileBackend) delete(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("account ID %q is not valid", id)
	}
	root, err := f.open(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(id + ext); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return private.SyncDir(root)
}

// CreatePrivate writes b to a new file at path that only core's user can read, made as a
// record file is: mode 0600, and on Windows the protected access list from the moment it
// exists. It fails with an error that is fs.ErrExist when something is at path already, and
// never leaves part of b at path. The folder must exist.
func CreatePrivate(path string, b []byte) error {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp, tmpName, err := private.CreateTemp(root, dir, tmpPrefix)
	if err != nil {
		return err
	}
	defer root.Remove(tmpName) // after the link below, path keeps the file
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// A link, unlike a rename, never replaces what is at path.
	if err := root.Link(tmpName, name); err != nil {
		return err
	}
	return private.SyncDir(root)
}
