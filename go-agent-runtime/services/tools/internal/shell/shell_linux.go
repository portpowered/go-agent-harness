//go:build !windows

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

	// Kill the entire process group spawned by the shell command.
	groupErr := syscall.Kill(-pid, syscall.SIGKILL)
	// Fallback kill on the shell process itself.
	return errors.Join(groupErr, cmd.Process.Kill())
}
