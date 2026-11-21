//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !windows

package main

import "os"

// terminalNoEcho has no terminal to read: the value is shown as it is typed.
func terminalNoEcho(*os.File) func() { return nil }
