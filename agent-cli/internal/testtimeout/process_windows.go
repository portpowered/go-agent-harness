//go:build windows

package testtimeout

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func prepareCommand(cmd *exec.Cmd) {}

func terminateCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}
