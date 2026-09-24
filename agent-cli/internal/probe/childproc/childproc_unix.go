//go:build !windows

package childproc

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const descendantCheckGrace = 100 * time.Millisecond

// Prepare places the child in its own process group so Terminate can reach
// its descendants.
func Prepare(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Terminate kills the child's process group, falling back to the child
// itself. An already-exited process is not an error.
func Terminate(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
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

// DescendantsAlive reports whether any member of the child's process group
// remains after the child was reaped.
func DescendantsAlive(command *exec.Cmd, childWaited bool) bool {
	if command == nil || command.Process == nil {
		return false
	}
	if !childWaited {
		return true
	}
	// A SIGINT can reap the group leader before the kernel tears down its
	// process group. Give that teardown a short bounded grace period so a
	// transient group membership is not recorded as an orphan.
	deadline := time.Now().Add(descendantCheckGrace)
	for {
		err := syscall.Kill(-command.Process.Pid, 0)
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(time.Millisecond)
	}
}
