//go:build !linux

package host

import "os"

func unreadBytes(*os.File) (int, bool) { return 0, false }
