//go:build !windows

package testtimeout

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateCommand(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGKILL)
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	var groupErr error
	if pid > 0 {
		groupErr = syscall.Kill(-pid, signal)
		if errors.Is(groupErr, syscall.ESRCH) {
			groupErr = nil
		}
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		if groupErr == nil {
			groupErr = err
		}
	}
	return groupErr
}
