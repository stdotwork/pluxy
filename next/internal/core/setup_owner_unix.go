//go:build !windows

package core

import (
	"os"
	"syscall"
)

const readNoWait = syscall.O_NONBLOCK

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
