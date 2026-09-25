// Package childproc holds the process-control and pipe helpers used by the
// probe's shipped-CLI duplex runner: process-group preparation and teardown,
// exit-code extraction, paced pipe writes, bounded output capture, and the
// classification of expected pipe and cancellation errors.
package childproc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExitCode returns the child's exit code, preferring the reaped process state
// and falling back to an *exec.ExitError. It returns -1 when neither exists.
func ExitCode(child *exec.Cmd, waitErr error) int {
	if child != nil && child.ProcessState != nil {
		return child.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// WaitUntil blocks until target or until ctx is done.
func WaitUntil(ctx context.Context, target time.Time) error {
	delay := time.Until(target)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WriteAll writes every byte of data, reporting io.ErrShortWrite when the
// writer makes no progress without an error.
func WriteAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// IsPipeClosure reports whether err is a closed or broken pipe.
func IsPipeClosure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || strings.Contains(strings.ToLower(err.Error()), "broken pipe")
}

// IsCancellation reports whether err is the expected consequence of ctx
// ending: a context error or a pipe closed during cancellation.
func IsCancellation(ctx context.Context, err error) bool {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

// IsSignalShutdown reports whether err is a pipe closed by a requested
// signal shutdown.
func IsSignalShutdown(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

// FormatCommand renders command and its quoted arguments for evidence.
func FormatCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, command)
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

// Capture retains at most limit bytes of a drained stream and records
// whether any bytes were discarded. A negative limit retains nothing.
type Capture struct {
	mu             sync.Mutex
	limit          int64
	data           bytes.Buffer
	truncatedValue bool
}

// NewCapture returns a capture bounded by limit bytes.
func NewCapture(limit int64) *Capture { return &Capture{limit: limit} }

// Append retains as much of data as the limit allows.
func (c *Capture) Append(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit < 0 {
		return
	}
	remaining := c.limit - int64(c.data.Len())
	if remaining <= 0 {
		if len(data) > 0 {
			c.truncatedValue = true
		}
		return
	}
	if int64(len(data)) > remaining {
		c.data.Write(data[:remaining])
		c.truncatedValue = true
		return
	}
	c.data.Write(data)
}

// Bytes returns a copy of the retained bytes.
func (c *Capture) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data.Bytes()...)
}

// Truncated reports whether any appended bytes were discarded.
func (c *Capture) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncatedValue
}
