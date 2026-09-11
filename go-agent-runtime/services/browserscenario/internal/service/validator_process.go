package service

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
)

const browserConversationValidatorCleanupGrace = 150 * time.Millisecond

type browserConversationValidatorProcessResult struct {
	stdout          []byte
	stdoutTruncated bool
	stderrTruncated bool
	err             error
}

func runBrowserConversationValidator(ctx context.Context, command []string, dir string, env []string, payload []byte, timeout time.Duration) browserConversationValidatorProcessResult {
	if ctx == nil {
		ctx = context.Background()
	}
	boundedContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	process := exec.Command(command[0], command[1:]...)
	process.Dir = dir
	if env != nil {
		process.Env = append([]string(nil), env...)
	}
	configureBrowserConversationProcess(process)
	stdout := &browserConversationBoundedBuffer{limit: maxBrowserConversationValidatorOutput}
	stderr := &browserConversationBoundedBuffer{limit: maxBrowserConversationValidatorOutput}
	process.Stdin = bytes.NewReader(payload)
	process.Stdout = stdout
	process.Stderr = stderr
	if err := process.Start(); err != nil {
		return browserConversationValidatorProcessResult{err: errors.Join(browserscenario.ErrBrowserConversationValidatorStart, err)}
	}

	waited := make(chan error, 1)
	go func() { waited <- process.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			return browserConversationValidatorProcessResult{
				stdout: stdout.Bytes(), stdoutTruncated: stdout.truncated, stderrTruncated: stderr.truncated,
				err: errors.Join(browserscenario.ErrBrowserConversationValidatorFailed, err),
			}
		}
		return browserConversationValidatorProcessResult{stdout: stdout.Bytes(), stdoutTruncated: stdout.truncated, stderrTruncated: stderr.truncated}
	case <-boundedContext.Done():
		terminateBrowserConversationProcessGroup(process)
		processExited := waitBrowserConversationProcess(waited, browserConversationValidatorCleanupGrace)
		groupGone := waitBrowserConversationProcessGroupGone(process, browserConversationValidatorCleanupGrace)
		if !processExited || !groupGone {
			killBrowserConversationProcessGroup(process)
			_ = waitBrowserConversationProcess(waited, time.Second)
			_ = waitBrowserConversationProcessGroupGone(process, time.Second)
		}
		return browserConversationValidatorProcessResult{err: browserscenario.ErrBrowserConversationValidatorTimeout}
	}
}

func waitBrowserConversationProcess(waited <-chan error, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-waited:
		return true
	case <-timer.C:
		return false
	}
}

func waitBrowserConversationProcessGroupGone(command *exec.Cmd, duration time.Duration) bool {
	deadline := time.Now().Add(duration)
	for {
		if !browserConversationProcessGroupExists(command) {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.NewTimer(minBrowserConversationDuration(remaining, 10*time.Millisecond))
		<-timer.C
	}
}

func minBrowserConversationDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
