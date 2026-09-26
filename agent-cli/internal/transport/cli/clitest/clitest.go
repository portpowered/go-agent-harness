// Package clitest runs the agent CLI entrypoint inside the test process.
//
// Run drives cli.Execute, the path cmd/agent uses, over a CLI composed exactly
// like the shipped binary, with in-memory standard streams instead of a built
// executable and pipes. Test wraps a test body in a testing/synctest bubble,
// so the command's timers, tickers, sleeps and context deadlines advance on the
// bubble's virtual clock: a 14-second paced audio stream or a 30-second
// max-duration bound completes as soon as every goroutine is idle.
//
// A bubble only advances while its goroutines are durably blocked. Commands
// run here must therefore reach providers and devices through in-memory
// transports (replay captures, injected dialers and devices), never through
// real sockets, subprocesses or cgo audio.
package clitest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

// Invocation is one in-process CLI run.
type Invocation struct {
	// Args are the arguments after the program name.
	Args []string
	// Stdin is the command's standard input; nil reads as empty.
	Stdin io.Reader
	// Timeout, when positive, bounds the run; exceeding it fails the test.
	// Inside a bubble the bound is virtual time, so a stuck command fails
	// as soon as every goroutine is idle.
	Timeout time.Duration
}

// Result is the observable outcome of a finished run, shaped like a process.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Run composes the CLI like cmd/agent (wire.InitializeAgentCLI) and executes
// inv to completion under t.Context(). A composition failure or an exceeded
// Timeout fails the test; command failures are reported through ExitCode and
// the captured streams, as the process boundary reports them.
func Run(t testing.TB, inv Invocation) Result {
	t.Helper()
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("compose agent CLI: %v", err)
	}
	ctx, cancel := t.Context(), context.CancelFunc(func() {})
	if inv.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, inv.Timeout)
	}
	defer cancel()
	stdin := inv.Stdin
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	var stdout, stderr syncBuffer
	code := cli.Execute(ctx, agentCLI.Generate(), cli.Invocation{
		Args:   inv.Args,
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("agent %s exceeded %s\nstdout:\n%s\nstderr:\n%s", strings.Join(inv.Args, " "), inv.Timeout, stdout.String(), stderr.String())
	}
	return Result{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// Test runs body in a synctest bubble with virtual time. Process-wide
// helpers that start a goroutine on first use are started outside the bubble
// first, so the bubble never owns a goroutine that blocks on the OS.
func Test(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	primeProcessGoroutines()
	synctest.Test(t, body)
}

// Subtest runs body as the named subtest of t in its own bubble. A bubble
// cannot start subtests, so table and scenario tests wrap each case.
func Subtest(t *testing.T, name string, body func(t *testing.T)) bool {
	t.Helper()
	return t.Run(name, func(t *testing.T) { Test(t, body) })
}

// primeProcessGoroutines starts os/signal's delivery loop. The loop starts
// once, on the first signal.Notify, and then blocks in the runtime on OS
// signals; started inside a bubble it would belong to that bubble and keep it
// from ever being idle, so virtual time would never advance. Registering and
// stopping a channel is idempotent after the first call.
func primeProcessGoroutines() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	signal.Stop(signals)
}

// syncBuffer is a goroutine-safe output capture.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
