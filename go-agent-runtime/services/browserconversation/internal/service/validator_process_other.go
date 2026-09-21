//go:build windows || plan9 || js

package service

import (
	"errors"
	"os"
	"os/exec"
)

func configureBrowserConversationProcess(*exec.Cmd) {}

func terminateBrowserConversationProcessGroup(command *exec.Cmd) error {
	if command.Process != nil {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}

func killBrowserConversationProcessGroup(command *exec.Cmd) error {
	if command.Process != nil {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}

func browserConversationProcessGroupExists(*exec.Cmd) bool { return false }
