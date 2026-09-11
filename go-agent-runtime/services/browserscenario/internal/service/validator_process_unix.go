//go:build aix || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package service

import (
	"os/exec"
	"syscall"
)

func configureBrowserConversationProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateBrowserConversationProcessGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
}

func killBrowserConversationProcessGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

func browserConversationProcessGroupExists(command *exec.Cmd) bool {
	if command == nil || command.Process == nil {
		return false
	}
	err := syscall.Kill(-command.Process.Pid, 0)
	return err == nil || err == syscall.EPERM
}
