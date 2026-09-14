//go:build aix || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package service

import (
	"os/exec"
	"syscall"
)

func configureBrowserConversationProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateBrowserConversationProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func killBrowserConversationProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func browserConversationProcessGroupExists(command *exec.Cmd) bool {
	if command == nil || command.Process == nil {
		return false
	}
	err := syscall.Kill(-command.Process.Pid, 0)
	return err == nil || err == syscall.EPERM
}
