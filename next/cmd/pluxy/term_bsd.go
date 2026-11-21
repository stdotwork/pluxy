//go:build darwin || freebsd || netbsd || openbsd

package main

import "syscall"

// The ioctls that read and write a terminal's settings (term_unix.go).
const (
	termiosGet = syscall.TIOCGETA
	termiosSet = syscall.TIOCSETA
)
