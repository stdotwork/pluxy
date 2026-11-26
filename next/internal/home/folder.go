package home

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// OpenDir opens dir, the folder a first config is about to be written in, as a root, after
// making it when it is missing and checking that, by its owner and permissions, no other user can
// add or replace files in it, since the config holds the management key. Only dir itself is
// checked, never the folders above it (some systems make /home a link), and only by its owner
// and mode bits on unix: an extended access list on macOS is not read.
//
// On unix a missing folder is made 0700 (with any missing parents), and an existing one is
// refused when it is a link, when another user owns it, or when its group or others can write in
// it; it is never changed. On Windows the folder is a private folder (private.Dir.Open): a link or
// a folder another account owns is refused, and one whose access list is not private is given
// the private list. A refusal is a *Refusal. The root is the folder checked, not one swapped in
// since.
func OpenDir(dir string) (*os.Root, error) { return openDir(dir) }

// Refusal is OpenDir's error for a folder it will not write a config in.
type Refusal struct {
	// Dir is the folder.
	Dir string
	// Link: it is a link. Owner, when set: the user that owns it instead. Mode, when set: its
	// permissions, which let others write in it. Err: Windows' private-folder check refused it.
	Link  bool
	Owner string
	Mode  fs.FileMode
	Err   error
}

func (r *Refusal) Error() string { return r.Text(func(s string) string { return s }) }

// Text is the refusal with its foreign text (the folder, the owner's name, the Windows check's
// message) written by show, as a terminal shows it. It names what to do: point PLUXY_HOME or
// -config elsewhere, or for a folder others can write in, the command that stops it. That command
// is left out when show changes the folder's name, since what is shown would then not run.
func (r *Refusal) Text(show func(string) string) string {
	why := ""
	switch {
	case r.Link:
		why = "it is a link. Point PLUXY_HOME or -config at the folder itself."
	case r.Owner != "":
		why = "it belongs to " + show(r.Owner) + ", not to you, so they could change your keys. Use a folder of your own (PLUXY_HOME or -config)."
	case r.Mode != 0 && show(r.Dir) != r.Dir:
		why = fmt.Sprintf("other users can add or replace files in it (mode %#o), and the config holds your keys. "+
			"Use another folder (PLUXY_HOME or -config).", r.Mode.Perm())
	case r.Mode != 0:
		why = fmt.Sprintf("other users can add or replace files in it (mode %#o), and the config holds your keys. "+
			"Run chmod go-w %s and try again, or use another folder.", r.Mode.Perm(), show(shellArg(r.Dir)))
	case r.Err != nil:
		why = show(r.Err.Error())
	}
	return "not writing a config in " + show(r.Dir) + ": " + why
}

func (r *Refusal) Unwrap() error { return r.Err }

// shellArg is s as one argument of a POSIX shell command: as it is when every byte is one that
// shell leaves alone, else in single quotes.
func shellArg(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+,:@%=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
