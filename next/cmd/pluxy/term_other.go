//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !windows

package main

import "os"

// isTerminalFile falls back to the character-device check where no terminal query is available.
func isTerminalFile(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// terminalNoEcho has no terminal to read: the value is shown as it is typed.
func terminalNoEcho(*os.File) func() { return nil }
