//go:build linux || darwin || freebsd || netbsd || openbsd

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminalFile reports whether f is a terminal (takes the termios ioctl).
func isTerminalFile(f *os.File) bool {
	var tio syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), termiosGet, uintptr(unsafe.Pointer(&tio)), 0, 0, 0)
	return errno == 0
}

// terminalNoEcho keeps the terminal's own echo of what is typed on f off, so a secret value is not
// shown as it is typed, and returns the call that turns it back on. It returns nil when f is not a
// terminal (an ioctl it does not take).
func terminalNoEcho(f *os.File) func() {
	fd := f.Fd()
	var old syscall.Termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, termiosGet, uintptr(unsafe.Pointer(&old)), 0, 0, 0); errno != 0 {
		return nil
	}
	hidden := old
	hidden.Lflag &^= syscall.ECHO
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, termiosSet, uintptr(unsafe.Pointer(&hidden)), 0, 0, 0); errno != 0 {
		return nil
	}
	return func() {
		_, _, _ = syscall.Syscall6(syscall.SYS_IOCTL, fd, termiosSet, uintptr(unsafe.Pointer(&old)), 0, 0, 0)
	}
}
