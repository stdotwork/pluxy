package host

import (
	"os"
	"syscall"
	"unsafe"
)

// unreadBytes returns how many bytes wait unread in the pipe f, and whether it could tell.
func unreadBytes(f *os.File) (int, bool) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, false
	}
	var n int32
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCINQ, uintptr(unsafe.Pointer(&n)))
	}); err != nil || errno != 0 {
		return 0, false
	}
	return int(n), true
}
