//go:build !windows

package core

import (
	"os/exec"
	"syscall"
)

// configureProbeCmd starts the probe in a process group of its own and makes its cancel kill
// the whole group: a child the probe started is ended with it.
func configureProbeCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
