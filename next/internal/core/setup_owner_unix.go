//go:build !windows

package core

import (
	"os"
	"syscall"
)

// readNoWait opens a tool's settings file without waiting for a writer (see openClientFile).
const readNoWait = syscall.O_NONBLOCK

// ownedByUser reports whether f belongs to the user pluxy runs as.
func ownedByUser(f *os.File) (bool, error) {
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}
	return int(st.Uid) == os.Geteuid(), nil
}
