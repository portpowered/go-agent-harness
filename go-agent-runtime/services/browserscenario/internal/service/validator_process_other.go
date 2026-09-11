//go:build windows || plan9 || js

package service

import "os/exec"

func configureBrowserConversationProcess(*exec.Cmd) {}

func terminateBrowserConversationProcessGroup(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}

func killBrowserConversationProcessGroup(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}

func browserConversationProcessGroupExists(*exec.Cmd) bool { return false }
