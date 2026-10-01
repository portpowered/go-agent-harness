//go:build windows

package shell

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

func prepareCommandForTermination(cmd *exec.Cmd) {
	// no-op on Windows
}

// taskkillTimeout bounds the process-tree kill once the command's own
// context has ended.
const taskkillTimeout = 5 * time.Second

// terminateProcessTree runs during cancellation, so taskkill gets a bounded
// context detached from the already-ended operation context.
func terminateProcessTree(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskkillTimeout)
	defer cancel()
	taskkillErr := exec.CommandContext(killCtx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	return errors.Join(taskkillErr, cmd.Process.Kill())
}
