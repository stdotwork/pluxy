//go:build windows

package core

import (
	"os"
	"os/exec"
	"strconv"
)

// Windows opens no FIFO in a folder to wait on.
func openClientFile(path string) (*os.File, error) {
	return os.Open(path)
}

func configureProbeCmd(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
