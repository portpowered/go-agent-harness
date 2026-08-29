//go:build windows

package probe

import "os/exec"

func prepareDuplexCommand(*exec.Cmd) {}

func terminateDuplexCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil || command.ProcessState != nil && command.ProcessState.Exited() {
		return nil
	}
	return command.Process.Kill()
}
