//go:build darwin

package shell

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepareCommandForTermination(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	return errors.Join(syscall.Kill(-pid, syscall.SIGKILL), cmd.Process.Kill())
}
