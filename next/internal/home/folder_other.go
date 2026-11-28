//go:build !windows

package home

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/stdotwork/pluxy/next/internal/private"
)

var euid = os.Geteuid

func openDir(dir string) (*os.Root, error) {
	st, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, private.DirMode); err != nil {
			return nil, err
		}
		st, err = os.Lstat(dir)
	}
	if err != nil {
		return nil, err
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return nil, &Refusal{Dir: dir, Link: true}
	}
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) { // EMLINK: FreeBSD's ELOOP for O_NOFOLLOW
		return nil, &Refusal{Dir: dir, Link: true}
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err = f.Stat(); err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != euid() {
		name := "user ID " + strconv.FormatUint(uint64(sys.Uid), 10)
		if u, err := user.LookupId(strconv.FormatUint(uint64(sys.Uid), 10)); err == nil && u.Username != "" {
			name = u.Username
		}
		return nil, &Refusal{Dir: dir, Owner: name}
	}
	if st.Mode().Perm()&0o022 != 0 {
		return nil, &Refusal{Dir: dir, Mode: st.Mode().Perm()}
	}
	return private.Dir{Path: dir}.Root(f)
}
