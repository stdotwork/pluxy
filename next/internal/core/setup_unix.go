//go:build !windows

package core

import (
	"os"
	"os/exec"
	"syscall"
)

// openClientFile opens a client's config file for reading without waiting for a writer:
// opening a FIFO waits for one, which would hold the scan until a writer comes or core stops.
func openClientFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// configureProbeCmd starts the probe in a process group of its own and makes its cancel kill
// the whole group: a child the probe started is ended with it.
func configureProbeCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
