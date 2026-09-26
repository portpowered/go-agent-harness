// Package clitest runs the agent CLI entrypoint inside the test process.
//
// Run and Start drive cli.Execute, the path cmd/agent uses, with in-memory
// standard streams instead of a built executable and pipes. Test wraps a test
// body in a testing/synctest bubble, so the command's timers, tickers, sleeps
// and context deadlines advance on the bubble's virtual clock: a 14-second
// paced audio stream or a 30-second max-duration bound completes as soon as
// every goroutine is idle.
//
// A bubble only advances while its goroutines are durably blocked. Commands
// run here must therefore reach providers and devices through in-memory
// transports (replay captures, PipeListener networks, injected devices),
// never through real sockets, subprocesses or cgo audio. A goroutine blocked
// on real I/O keeps the bubble from ever going idle, so virtual time stops
// and virtual deadlines never fire; Test's real-time watchdog then fails the
// run with a goroutine dump instead of letting it hang until the global
// -timeout.
//
// PipeListener streams approximate loopback TCP, not exactly: after the peer
// closed, the first write is silently dropped (TCP may already report
// EPIPE/ECONNRESET) and later writes report EPIPE; buffering is unbounded;
// there is no CloseWrite; and a read past its deadline still returns
// buffered bytes. See newStreamConnPair.
//
// Composition uses the production (strict) model validation. Only callers
// that swap components and must match a relaxed mock binary set
// RelaxModelValidation.
package clitest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
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
	// Timeout, when positive, bounds Run; exceeding it fails the test. Inside
	// a bubble the bound is virtual time: it fires once every goroutine is
	// durably blocked. A goroutine blocked on real I/O never lets that
	// happen; Test's real-time watchdog covers that case.
	Timeout time.Duration
	// Ports replace composition ports by name. Without replacements the CLI
	// is composed exactly like cmd/agent (wire.InitializeAgentCLI).
	Ports []wire.PortSwap
	// RelaxModelValidation selects the mock initializer's relaxed model
	// validation (wire.InitializeMockAgentCLIWithPorts). Leave it false unless
	// the run must match a mock binary that uses it: the default is the
	// strict validation the shipped binary applies.
	RelaxModelValidation bool
	// Configure, when set, adjusts the composed CLI before it runs.
	Configure func(*cli.AgentCLI)
	// Stdout and Stderr, when set, also receive the command's output as it is
	// written, for tests that react to streamed output.
	Stdout io.Writer
	Stderr io.Writer
}

// Result is the observable outcome of a finished run, shaped like a process.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Process is a command started by Start and running in the background.
type Process struct {
	done   chan struct{}
	cancel context.CancelFunc
	stdout syncBuffer
	stderr syncBuffer
	result Result
}

// Run composes the CLI (see Invocation.Ports) and executes inv to completion
// under t.Context(). A composition failure or an exceeded Timeout fails the
// test; command failures are reported through ExitCode and the captured
// streams, as the process boundary reports them.
func Run(t testing.TB, inv Invocation) Result {
	t.Helper()
	process := Start(t, inv)
	if inv.Timeout <= 0 {
		return process.Wait()
	}
	timer := time.NewTimer(inv.Timeout)
	defer timer.Stop()
	select {
	case <-process.Done():
		return process.Wait()
	case <-timer.C:
		process.Cancel()
		result := process.Wait()
		t.Fatalf("agent %s exceeded %s\nstdout:\n%s\nstderr:\n%s", strings.Join(inv.Args, " "), inv.Timeout, result.Stdout, result.Stderr)
		return result
	}
}

// Start composes the CLI and runs inv in the background under t.Context().
// A composition failure fails the test before anything runs.
func Start(t testing.TB, inv Invocation) *Process {
	t.Helper()
	agentCLI, err := compose(inv)
	if err != nil {
		t.Fatalf("compose agent CLI: %v", err)
	}
	if inv.Configure != nil {
		inv.Configure(agentCLI)
	}
	root := agentCLI.Generate()
	stdin := inv.Stdin
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	ctx, cancel := context.WithCancel(t.Context())
	process := &Process{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(process.done)
		defer cancel()
		code := cli.Execute(ctx, root, cli.Invocation{
			Args:   inv.Args,
			Stdin:  stdin,
			Stdout: tee(&process.stdout, inv.Stdout),
			Stderr: tee(&process.stderr, inv.Stderr),
		})
		process.result = Result{ExitCode: code, Stdout: process.stdout.String(), Stderr: process.stderr.String()}
	}()
	return process
}

// Done is closed when the command has returned.
func (p *Process) Done() <-chan struct{} { return p.done }

// Wait blocks until the command returns and reports its result.
func (p *Process) Wait() Result {
	<-p.done
	return p.result
}

// Cancel cancels the command's context, as a caller's cancellation would.
func (p *Process) Cancel() { p.cancel() }

func compose(inv Invocation) (*cli.AgentCLI, error) {
	switch {
	case inv.RelaxModelValidation:
		return wire.InitializeMockAgentCLIWithPorts(inv.Ports...)
	case len(inv.Ports) > 0:
		return wire.InitializeAgentCLIWithPorts(inv.Ports...)
	default:
		return wire.InitializeAgentCLI()
	}
}

func tee(capture *syncBuffer, observer io.Writer) io.Writer {
	if observer == nil {
		return capture
	}
	return io.MultiWriter(capture, observer)
}

// RealTimeLimit is how long one bubble may run in real time before the
// watchdog declares it stuck. Bubbles advance virtual time only while idle,
// so a legitimate test is CPU-bound and finishes far sooner; the limit leaves
// room for -race and a heavily loaded machine.
const RealTimeLimit = 2 * time.Minute

// stackDumpBytes bounds the watchdog's goroutine dump.
const stackDumpBytes = 16 << 20

// Test runs body in a synctest bubble with virtual time. Process-wide
// helpers that start a goroutine on first use are started outside the bubble
// first, so the bubble never owns a goroutine that blocks on the OS. A
// real-time watchdog fails the test binary with a goroutine dump if the
// bubble runs longer than RealTimeLimit.
func Test(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	testWithin(t, RealTimeLimit, body)
}

func testWithin(t *testing.T, limit time.Duration, body func(t *testing.T)) {
	t.Helper()
	primeProcessGoroutines()
	name := t.Name()
	watchdog := time.AfterFunc(limit, func() { failStuckBubble(name, limit) })
	defer watchdog.Stop()
	synctest.Test(t, body)
}

// failStuckBubble runs on the watchdog's real timer, outside the bubble. The
// bubble's goroutines cannot be unblocked from here, so it dumps every
// goroutine and panics, failing the test binary with the stuck I/O visible.
func failStuckBubble(name string, limit time.Duration) {
	stacks := make([]byte, stackDumpBytes)
	stacks = stacks[:runtime.Stack(stacks, true)]
	_, _ = fmt.Fprintf(os.Stderr, "%s\n", stacks) // best-effort diagnostics before the panic
	panic(fmt.Sprintf("clitest: %s ran %s of real time and its synctest bubble never went idle: "+
		"real I/O (socket, exec or cgo) inside clitest.Test? goroutines dumped above", name, limit))
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
