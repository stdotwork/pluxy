//go:build windows

package core

import (
	"os"
	"os/exec"
	"strconv"
)

// openClientFile opens a client's config file for reading. Windows opens no FIFO in a folder
// to wait on: readClientFile refuses whatever is not a file, a device or pipe included.
func openClientFile(path string) (*os.File, error) {
	return os.Open(path)
}

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
