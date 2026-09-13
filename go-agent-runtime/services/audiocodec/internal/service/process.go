package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
)

type processRunner struct {
	executable string
	lookPath   func(string) (string, error)
	command    func(context.Context, string, ...string) command
}

type command interface {
	setStdin(io.Reader)
	setStdout(io.Writer)
	setStderr(io.Writer)
	start() error
	wait() error
}

type execCommand struct {
	cmd *exec.Cmd
}

func (c *execCommand) setStdin(reader io.Reader)  { c.cmd.Stdin = reader }
func (c *execCommand) setStdout(writer io.Writer) { c.cmd.Stdout = writer }
func (c *execCommand) setStderr(writer io.Writer) { c.cmd.Stderr = writer }
func (c *execCommand) start() error               { return c.cmd.Start() }
func (c *execCommand) wait() error                { return c.cmd.Wait() }

func newProcessRunner(executable string) *processRunner {
	return &processRunner{
		executable: executable,
		lookPath:   exec.LookPath,
		command: func(ctx context.Context, path string, args ...string) command {
			return &execCommand{cmd: exec.CommandContext(ctx, path, args...)}
		},
	}
}

func (r *processRunner) run(ctx context.Context, inputPath string, limits audiocodec.Limits) (runResult, error) {
	if err := ctx.Err(); err != nil {
		return runResult{}, newError(audiocodec.ErrorCanceled, err, "decoder context ended before start")
	}
	path, err := r.lookPath(r.executable)
	if err != nil {
		return runResult{}, newError(audiocodec.ErrorExecutableLookup, err, r.executable)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		return runResult{}, newError(audiocodec.ErrorInputFile, err, "open temporary input")
	}
	defer input.Close()

	cmd := r.command(ctx, path, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", inputPath, "-f", "s16le", "-ac", "1", "-ar", "16000", "-")
	stdout := newBoundedBuffer(limits.MaxOutputBytes)
	stderr := newBoundedBuffer(limits.MaxStderrBytes)
	cmd.setStdin(input)
	cmd.setStdout(stdout)
	cmd.setStderr(stderr)
	if err := cmd.start(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return runResult{}, newError(audiocodec.ErrorCanceled, contextErr, "decoder context ended during start")
		}
		return runResult{}, newError(audiocodec.ErrorProcessStart, err, "start "+r.executable)
	}
	waitErr := cmd.wait()
	if stdout.exceeded {
		return runResult{}, newError(audiocodec.ErrorOutputTooLarge, stdout.limitError(), "capture decoder stdout")
	}
	if stderr.exceeded {
		return runResult{}, newError(audiocodec.ErrorStderrTooLarge, stderr.limitError(), "capture decoder stderr")
	}
	if waitErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return runResult{}, newError(audiocodec.ErrorCanceled, ctx.Err(), "decoder context ended")
		}
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			detail := "decoder exited unsuccessfully"
			if stderr.Len() != 0 {
				detail += ": " + stderr.String()
			}
			return runResult{}, newError(audiocodec.ErrorDecode, waitErr, detail)
		}
		return runResult{}, newError(audiocodec.ErrorProcessWait, waitErr, "wait for "+r.executable)
	}
	return runResult{stdout: stdout.Bytes()}, nil
}

type boundedBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{max: max}
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	remaining := b.max - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, io.ErrShortBuffer
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.exceeded = true
		return remaining, io.ErrShortBuffer
	}
	return b.Buffer.Write(data)
}

func (b *boundedBuffer) limitError() error {
	return fmt.Errorf("captured %d bytes, limit %d", b.Len(), b.max)
}
