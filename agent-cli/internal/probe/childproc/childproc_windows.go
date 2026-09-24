//go:build windows

package childproc

import (
	"errors"
	"os"
	"os/exec"
)

// Prepare needs no process-group setup on Windows.
func Prepare(*exec.Cmd) {}

// Terminate kills the child. An already-exited process is not an error.
func Terminate(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	err := command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// DescendantsAlive reports whether the child itself was left unreaped.
func DescendantsAlive(command *exec.Cmd, childWaited bool) bool {
	if command == nil || command.Process == nil {
		return false
	}
	return !childWaited
}
