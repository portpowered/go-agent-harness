//go:build !windows

package probe

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepareDuplexCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateDuplexCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil || command.ProcessState != nil && command.ProcessState.Exited() {
		return nil
	}
	pid := command.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		if directErr := command.Process.Kill(); directErr != nil && !errors.Is(directErr, syscall.ESRCH) {
			return errors.Join(err, directErr)
		}
	}
	return nil
}
