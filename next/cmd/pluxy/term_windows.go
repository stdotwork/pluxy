//go:build windows

package main

import (
	"os"
	"syscall"
)

// enableEchoInput is the console mode bit that echoes typed input.
const enableEchoInput = 0x0004

// setConsoleMode is SetConsoleMode of kernel32.dll, which syscall does not wrap (it wraps
// GetConsoleMode only). A zero return is an error; the call's error is the one to report.
func setConsoleMode(h syscall.Handle, mode uint32) error {
	r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode))
	if r == 0 {
		return err
	}
	return nil
}

var procSetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

// isTerminalFile reports whether f is a Windows console.
func isTerminalFile(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

// terminalNoEcho keeps the console's own echo of what is typed on f off, so a secret value is not
// shown as it is typed, and returns the call that turns it back on. It returns nil when f is not a
// console.
func terminalNoEcho(f *os.File) func() {
	h := syscall.Handle(f.Fd())
	var old uint32
	if err := syscall.GetConsoleMode(h, &old); err != nil || old&enableEchoInput == 0 {
		return nil
	}
	if err := setConsoleMode(h, old&^enableEchoInput); err != nil {
		return nil
	}
	return func() { _ = setConsoleMode(h, old) }
}
