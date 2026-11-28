package home

import (
	"os"

	"github.com/stdotwork/pluxy/next/internal/private"
)

func openDir(dir string) (*os.Root, error) {
	d := private.Dir{Path: dir, Setting: "PLUXY_HOME or -config"}
	checked, err := d.Open(true)
	if err != nil {
		return nil, &Refusal{Dir: dir, Err: err}
	}
	defer checked.Close()
	return d.Root(checked)
}
