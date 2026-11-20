//go:build windows

package core

import (
	"os/exec"
	"strconv"
)

// configureProbeCmd makes the probe's cancel end its whole process tree: Process.Kill would
// end the probe alone and leave a child it started running.
func configureProbeCmd(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
