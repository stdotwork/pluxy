package home

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
)

func OpenDir(dir string) (*os.Root, error) { return openDir(dir) }

type Refusal struct {
	Dir   string
	Link  bool
	Owner string
	Mode  fs.FileMode
	Err   error
}

func (r *Refusal) Error() string { return r.Text(func(s string) string { return s }) }

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

func shellArg(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+,:@%=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
