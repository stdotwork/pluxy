//go:build linux

package main

import "syscall"

// The ioctls that read and write a terminal's settings (term_unix.go).
const (
	termiosGet = syscall.TCGETS
	termiosSet = syscall.TCSETS
)
