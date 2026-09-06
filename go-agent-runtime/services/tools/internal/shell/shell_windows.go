//go:build windows

package shell

import (
	"errors"
	"os/exec"
	"strconv"
)

func prepareCommandForTermination(cmd *exec.Cmd) {
	// no-op on Windows
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	taskkillErr := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	return errors.Join(taskkillErr, cmd.Process.Kill())
}
