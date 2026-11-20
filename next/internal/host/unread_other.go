//go:build !linux

package host

import "os"

// unreadBytes cannot tell how many bytes wait in a pipe on this system.
func unreadBytes(*os.File) (int, bool) { return 0, false }
