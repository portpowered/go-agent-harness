//go:build windows

package testtimeout

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func prepareCommand(cmd *exec.Cmd) {}

func terminateCommand(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}
