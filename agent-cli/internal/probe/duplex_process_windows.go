//go:build windows

package probe

import (
	"errors"
	"os"
	"os/exec"
)

func prepareDuplexCommand(*exec.Cmd) {}

func terminateDuplexCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	err := command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func duplexDescendantsAlive(command *exec.Cmd, childWaited bool) bool {
	if command == nil || command.Process == nil {
		return false
	}
	return !childWaited
}
