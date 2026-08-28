// Package testtimeout runs test commands with a finite outer budget and
// fail-closed descendant cleanup. The Go test -timeout flag remains part of
// the command contract; this boundary handles processes that survive a test
// binary's own timeout or keep its output pipes open.
package testtimeout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const waitAfterTermination = 2 * time.Second

// Config describes one command execution. Timeout must be positive so every
// production caller has an explicit finite cleanup boundary.
type Config struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
	Label   string
	Timeout time.Duration
	Stdout  io.Writer
	Stderr  io.Writer
}

// Result contains the observable command outcome and captured output. Output
// is captured even when Stdout or Stderr are also configured for streaming.
type Result struct {
	Command  string
	PID      int
	ExitCode int
	Output   string
	Duration time.Duration
	TimedOut bool
}

// Error is returned for an unsuccessful command. A timeout error includes the
// command PID and cleanup outcome so a blocked test can be assigned to its
// owner from ordinary CI output.
type Error struct {
	Label       string
	Command     string
	PID         int
	ExitCode    int
	Timeout     time.Duration
	TimedOut    bool
	Termination string
	Cause       error
}

func (e *Error) Error() string {
	label := e.Label
	if label == "" {
		label = "test command"
	}
	if e.TimedOut {
		termination := e.Termination
		if termination == "" {
			termination = "descendants terminated"
		}
		return fmt.Sprintf("%s timed out after %s: command=%s pid=%d; %s", label, e.Timeout, e.Command, e.PID, termination)
	}
	if e.ExitCode >= 0 {
		return fmt.Sprintf("%s exited with status %d: command=%s pid=%d", label, e.ExitCode, e.Command, e.PID)
	}
	return fmt.Sprintf("%s failed: command=%s pid=%d: %v", label, e.Command, e.PID, e.Cause)
}

// Unwrap exposes the underlying exec or cleanup failure to callers.
func (e *Error) Unwrap() error { return e.Cause }

// Run starts cfg.Command in an isolated process group and waits for it to
// finish. When the finite timeout or parent context expires, the process
// group is terminated before the result is returned.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return Result{}, errors.New("test timeout runner requires a command")
	}
	if cfg.Timeout <= 0 {
		return Result{}, fmt.Errorf("test timeout runner requires a positive finite timeout, got %s", cfg.Timeout)
	}

	commandText := formatCommand(cfg.Command, cfg.Args)
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	if cfg.Env != nil {
		cmd.Env = append([]string(nil), cfg.Env...)
	}
	prepareCommand(cmd)

	var output lockedBuffer
	cmd.Stdout = outputWriter(&output, cfg.Stdout)
	cmd.Stderr = outputWriter(&output, cfg.Stderr)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{Command: commandText, ExitCode: -1, Output: output.String(), Duration: time.Since(started)}, &Error{
			Label:   cfg.Label,
			Command: commandText,
			PID:     processID(cmd),
			Cause:   err,
		}
	}

	result := Result{Command: commandText, PID: processID(cmd), ExitCode: -1}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(cfg.Timeout)
	defer timer.Stop()

	var commandErr error
	var timedOut bool
	var contextCanceled bool
	termination := ""
	select {
	case commandErr = <-done:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
		contextCanceled = true
		commandErr = ctx.Err()
	}

	if timedOut || contextCanceled {
		termination = "descendants terminated"
		cleanupErr := terminateCommand(cmd)
		if cleanupErr != nil {
			termination = "descendant termination reported: " + cleanupErr.Error()
		}
		select {
		case commandErr = <-done:
		case <-time.After(waitAfterTermination):
			if retryErr := terminateCommand(cmd); retryErr != nil {
				termination += "; retry termination reported: " + retryErr.Error()
			}
			select {
			case commandErr = <-done:
			case <-time.After(waitAfterTermination):
				termination += "; command wait exceeded cleanup grace"
				commandErr = errors.New("command wait exceeded cleanup grace")
			}
		}
		result.TimedOut = timedOut
		if commandErr == nil {
			commandErr = context.DeadlineExceeded
		}
	}

	result.Duration = time.Since(started)
	result.Output = output.String()
	result.ExitCode = exitCode(cmd, commandErr)
	if commandErr == nil {
		return result, nil
	}
	return result, &Error{
		Label:       cfg.Label,
		Command:     commandText,
		PID:         result.PID,
		ExitCode:    result.ExitCode,
		Timeout:     cfg.Timeout,
		TimedOut:    result.TimedOut,
		Termination: termination,
		Cause:       commandErr,
	}
}

func outputWriter(output *lockedBuffer, destination io.Writer) io.Writer {
	if destination == nil {
		return output
	}
	return io.MultiWriter(output, destination)
}

func formatCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, command)
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func processID(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return -1
	}
	return cmd.Process.Pid
}

func exitCode(cmd *exec.Cmd, err error) int {
	if cmd != nil && cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
