package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/childproc"
)

// duplexSession is the shared state of one running child: its pipes, pumps,
// termination request, and the facts later copied into DuplexRunResult.
type duplexSession struct {
	runCtx    context.Context
	cancelRun context.CancelFunc
	config    normalizedDuplexConfig
	child     *exec.Cmd
	stdin     io.WriteCloser
	startedAt time.Time

	deadlineReached atomic.Bool
	progress        *duplexProgressState
	stdoutCapture   *childproc.Capture
	stderrCapture   *childproc.Capture
	inputEventsMu   sync.Mutex
	inputEvents     []DuplexInputEvent
	inputFinished   atomic.Bool
	inputClosed     atomic.Bool
	stdoutClosed    atomic.Bool
	stderrClosed    atomic.Bool
	waitCount       atomic.Int32

	closeStdinOnce sync.Once
	closeStdinErr  error
	terminateOnce  sync.Once
	terminateErr   error

	signalSent           atomic.Bool
	terminationRequested atomic.Bool
	signalMu             sync.Mutex
	signalAt             time.Duration

	failureCh   chan error
	failureOnce sync.Once

	pumps         sync.WaitGroup
	outputPumps   sync.WaitGroup
	terminationWG sync.WaitGroup
}

// duplexFailureBuffer bounds the first recorded pump or signal failure.
const duplexFailureBuffer = 4

// duplexPumpCount is the stdout, stderr, and stdin pump goroutines.
const duplexPumpCount = 3

func newDuplexSession(runCtx context.Context, cancelRun context.CancelFunc, config normalizedDuplexConfig, child *exec.Cmd, stdin io.WriteCloser, startedAt time.Time) *duplexSession {
	session := &duplexSession{
		runCtx:        runCtx,
		cancelRun:     cancelRun,
		config:        config,
		child:         child,
		stdin:         stdin,
		startedAt:     startedAt,
		progress:      newDuplexProgressState(),
		stdoutCapture: childproc.NewCapture(config.MaxCapturedOutputBytes),
		stderrCapture: childproc.NewCapture(config.MaxCapturedOutputBytes),
		failureCh:     make(chan error, duplexFailureBuffer),
	}
	session.progress.setStartedAt(startedAt)
	return session
}

func (s *duplexSession) closeStdin() error {
	s.closeStdinOnce.Do(func() {
		s.closeStdinErr = s.stdin.Close()
		s.inputClosed.Store(true)
	})
	return s.closeStdinErr
}

func (s *duplexSession) terminate() error {
	s.terminateOnce.Do(func() { s.terminateErr = childproc.Terminate(s.child) })
	return s.terminateErr
}

func (s *duplexSession) recordFailure(failure error) {
	if errors.Is(failure, errDuplexInputClosed) {
		// A provider SESSION.CLOSE can make the shipped child exit while the
		// runner is still writing the trailing PCM frame. Let child.Wait
		// establish the terminal process result; an early child exit with no
		// observable output still fails closed as ErrDuplexInputIncomplete.
		return
	}
	if failure == nil || childproc.IsCancellation(s.runCtx, failure) || (s.terminationRequested.Load() && childproc.IsSignalShutdown(failure)) {
		return
	}
	s.failureOnce.Do(func() {
		s.failureCh <- failure
		s.cancelRun()
	})
}

func (s *duplexSession) startPumps(stdout, stderr io.Reader) {
	s.pumps.Add(duplexPumpCount)
	s.outputPumps.Add(2)
	go func() {
		defer s.pumps.Done()
		defer s.outputPumps.Done()
		if err := pumpDuplexOutput(s.runCtx, stdout, s.config.Output, s.stdoutCapture, s.progress, s.startedAt, true); err != nil {
			s.recordFailure(err)
		}
		s.progress.noteOutputClosed()
		s.stdoutClosed.Store(true)
	}()
	go func() {
		defer s.pumps.Done()
		defer s.outputPumps.Done()
		if err := pumpDuplexOutput(s.runCtx, stderr, s.config.ErrorOutput, s.stderrCapture, s.progress, s.startedAt, false); err != nil {
			s.recordFailure(err)
		}
		s.stderrClosed.Store(true)
	}()
	go func() {
		defer s.pumps.Done()
		if err := pumpDuplexInput(s.runCtx, s.stdin, s.config, s.progress, s.startedAt, &s.inputEventsMu, &s.inputEvents, &s.inputFinished, s.closeStdin); err != nil {
			s.recordFailure(err)
		}
	}()
}

// runSIGINTTermination waits for the configured output gate and then sends
// SIGINT to the child. The caller has already added it to terminationWG.
func (s *duplexSession) runSIGINTTermination() {
	defer s.terminationWG.Done()
	var waitErr error
	switch {
	case s.config.TerminationAfterOutputBytes > 0:
		waitErr = s.progress.waitForOutput(s.runCtx, s.config.TerminationAfterOutputBytes, false)
	case s.config.TerminationAfterOutputReads > 0:
		waitErr = s.progress.waitForOutput(s.runCtx, int64(s.config.TerminationAfterOutputReads), true)
	}
	if waitErr != nil {
		return
	}
	s.terminationRequested.Store(true)
	sent, err := sendDuplexSIGINT(s.child)
	if err != nil {
		s.terminationRequested.Store(false)
		s.recordFailure(duplexPipeError("send SIGINT", err))
		return
	}
	if !sent {
		s.terminationRequested.Store(false)
		return
	}
	s.signalSent.Store(true)
	s.signalMu.Lock()
	s.signalAt = time.Since(s.startedAt)
	s.signalMu.Unlock()
	// Stop feeding a process that has been asked to terminate. The input
	// pump treats the resulting closed-pipe write as expected signal
	// shutdown, while stdout/stderr continue draining concurrently. A close
	// failure is retained in closeStdinErr and reported with the run result.
	if err := s.closeStdin(); err != nil {
		return
	}
}

func (s *duplexSession) startWait() <-chan error {
	waitDone := make(chan error, 1)
	go func() {
		// StdoutPipe and StderrPipe require their readers to reach EOF before
		// Wait closes the pipe descriptors. Reaping concurrently can otherwise
		// discard the child's final audio/output chunk under scheduler load.
		s.outputPumps.Wait()
		s.waitCount.Add(1)
		waitDone <- s.child.Wait()
	}()
	return waitDone
}

func (s *duplexSession) fillResult(ctx context.Context, result *DuplexRunResult, processWaitOK bool, waitErr error) {
	result.Duration = time.Since(s.startedAt)
	result.ExitCode = childproc.ExitCode(s.child, waitErr)
	result.TimedOut = s.deadlineReached.Load()
	result.Cancelled = ctx.Err() != nil && !result.TimedOut
	result.SignalSent = s.signalSent.Load()
	if result.SignalSent {
		result.Signal = duplexSIGINTName
		s.signalMu.Lock()
		result.SignalAt = s.signalAt
		s.signalMu.Unlock()
	}
	result.ChildWaited = processWaitOK
	result.WaitCount = int(s.waitCount.Load())
	result.InputClosed = s.inputClosed.Load()
	result.InputFinished = s.inputFinished.Load()
	result.StdoutClosed = s.stdoutClosed.Load()
	result.StderrClosed = s.stderrClosed.Load()
	result.DescendantsAlive = childproc.DescendantsAlive(s.child, processWaitOK)
	result.ExitClassification = duplexExitClassification(*result, s.config.Termination, waitErr)
	result.Output = s.progress.outputEvents()
	if !s.inputFinished.Load() && result.ExitClassification == duplexExitNormal && result.StdoutClosed && len(result.Output) > 0 {
		// A normal child exit after observable stdout is a provider-owned
		// session boundary. The input pump may have been cancelled by the
		// runner's post-wait cleanup before it could mark the final byte as
		// finished, but the product response itself crossed the boundary.
		result.InputFinished = true
	}
	result.CapturedOutputTruncated = s.stdoutCapture.Truncated() || s.stderrCapture.Truncated()
	result.Stdout = s.stdoutCapture.Bytes()
	result.Stderr = s.stderrCapture.Bytes()
	s.inputEventsMu.Lock()
	result.Input = append([]DuplexInputEvent(nil), s.inputEvents...)
	s.inputEventsMu.Unlock()
}

// shutdownFailures reports the first pump failure and any failure to close,
// terminate, or reap the child.
func (s *duplexSession) shutdownFailures(result DuplexRunResult, processWaitOK bool, waitErr error) []error {
	var failures []error
	select {
	case failure := <-s.failureCh:
		failures = append(failures, failure)
	default:
	}
	// exec.Cmd.Wait closes the child-side pipe after the process exits. An
	// explicit close racing that cleanup can therefore report os.ErrClosed even
	// though the process was fully reaped; only surface close failures while the
	// child is still alive.
	if s.closeStdinErr != nil && !processWaitOK {
		failures = append(failures, duplexPipeError("close stdin", s.closeStdinErr))
	}
	if s.terminateErr != nil && !processWaitOK {
		failures = append(failures, duplexPipeError("terminate child", s.terminateErr))
	}
	if !processWaitOK {
		if result.TimedOut {
			failures = append(failures, fmt.Errorf("%w after %s", ErrDuplexChildSurvivedDeadline, s.config.MaxDuration))
		} else {
			failures = append(failures, fmt.Errorf("%w: %w", ErrDuplexShutdown, waitErr))
		}
	} else if result.TimedOut {
		failures = append(failures, fmt.Errorf("%w after %s", ErrDuplexDeadline, s.config.MaxDuration))
	}
	return failures
}

// exitFailures reports unsuccessful exits, cancellation, incomplete input,
// unjoined pumps, and truncated captures, in that order.
func (s *duplexSession) exitFailures(ctx context.Context, result DuplexRunResult, processWaitOK, pumpsJoined bool, waitErr error) []error {
	var failures []error
	if waitErr != nil && processWaitOK && !result.TimedOut && result.ExitClassification != duplexExitSIGINT && !isExpectedDuplexWaitClose(result, waitErr) {
		if result.ExitCode != 0 {
			failures = append(failures, fmt.Errorf("%w: exit code %d", ErrDuplexProcessExit, result.ExitCode))
		} else {
			failures = append(failures, duplexPipeError("wait for child", waitErr))
		}
	}
	if result.Cancelled {
		failures = append(failures, ctx.Err())
	}
	if processWaitOK && !result.InputFinished && !result.TimedOut && !result.Cancelled && result.ExitClassification != duplexExitSIGINT {
		failures = append(failures, ErrDuplexInputIncomplete)
	}
	if !pumpsJoined {
		failures = append(failures, fmt.Errorf("%w after %s", ErrDuplexShutdown, s.config.ShutdownGrace))
	}
	if result.CapturedOutputTruncated {
		failures = append(failures, ErrDuplexOutputCaptureLimit)
	}
	return failures
}

func waitForDuplexChild(
	runCtx context.Context,
	closeStdin func() error,
	terminate func() error,
	waitDone <-chan error,
	pumps *sync.WaitGroup,
	terminationWG *sync.WaitGroup,
	cancelRun context.CancelFunc,
	shutdownGrace time.Duration,
) (processWaitOK, pumpsJoined bool, waitErr error) {
	// A pipe write or a segment gate cannot be left waiting after cancellation.
	// Closing stdin unblocks an in-flight write, and killing the process group
	// closes inherited stdout/stderr descriptors held by descendants.
	watchDone := make(chan struct{})
	var watchWG sync.WaitGroup
	watchWG.Add(1)
	go func() {
		defer watchWG.Done()
		select {
		case <-runCtx.Done():
			runDuplexShutdownStep(closeStdin)
			runDuplexShutdownStep(terminate)
		case <-watchDone:
		}
	}()

	select {
	case waitErr = <-waitDone:
		processWaitOK = true
	case <-runCtx.Done():
		processWaitOK, waitErr = waitForDuplexProcess(waitDone, shutdownGrace, terminate)
	}

	// The process may exit before the script has delivered every segment. Close
	// the caller-owned write end so the input pump observes the premature EOF
	// rather than remaining blocked on a dead child.
	runDuplexShutdownStep(closeStdin)
	if processWaitOK {
		cancelRun()
	}
	close(watchDone)
	watchWG.Wait()

	pumpsDone := make(chan struct{})
	go func() {
		pumps.Wait()
		close(pumpsDone)
	}()
	select {
	case <-pumpsDone:
		pumpsJoined = true
	case <-time.After(shutdownGrace):
	}
	terminationWG.Wait()
	return processWaitOK, pumpsJoined, waitErr
}

func waitForDuplexProcess(waitDone <-chan error, grace time.Duration, terminate func() error) (bool, error) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-waitDone:
		return true, err
	case <-timer.C:
		runDuplexShutdownStep(terminate)
	}
	timer.Reset(grace)
	defer timer.Stop()
	select {
	case err := <-waitDone:
		return true, err
	case <-timer.C:
		return false, fmt.Errorf("%w after %s", ErrDuplexChildSurvivedDeadline, grace)
	}
}

// runDuplexShutdownStep invokes a once-guarded close or terminate step during
// shutdown. The step retains its own error (closeStdinErr or terminateErr),
// which is reported with the run result, so the return value is not needed.
func runDuplexShutdownStep(step func() error) {
	if err := step(); err != nil {
		return
	}
}
