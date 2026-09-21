package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
)

// prepareSessionStreamOutput gives an unowned stream its terminal renderer.
// The returned finalizer preserves transcript errors before publishing status.
func prepareSessionStreamOutput(out io.Writer, opts *sessionLoopOptions) (io.Writer, func(error) error) {
	if opts.terminalReporter != nil {
		return out, func(err error) error { return err }
	}
	reporter := newSessionTerminalReporter()
	opts.terminalReporter = reporter
	reporter.markRunStarted()
	terminalService := terminalwire.NewService()
	renderer := terminalService.NewTranscriptRenderer(out, reporter.observeStreamMessage)
	return renderer, func(runErr error) error {
		runErr = errors.Join(runErr, renderer.Finish())
		return errors.Join(runErr, reporter.publish(out, runErr))
	}
}

func newObservedSessionLoop(inferencer messages.SessionInferencer, opts sessionLoopOptions) (*agentloop.AgentLoop, *observedSessionInferencer, <-chan error, error) {
	observed := newObservedSessionInferencer(inferencer, opts.runtime)
	observed.progress = opts.observer
	if opts.observer != nil {
		opts.observer.setToolResultsEnabled(opts.ToolExecutor != nil)
	}
	loop, err := agentloop.New(duplexSessionLoopOptions(observed, opts)...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create session agent loop: %w", err)
	}
	var deviceErrors <-chan error
	if opts.rtcDeviceBinding != nil {
		deviceErrors = opts.rtcDeviceBinding.Errors()
	}
	return loop, observed, deviceErrors, nil
}

func sessionStreamDeadline(opts sessionLoopOptions) (<-chan time.Time, func(), error) {
	if opts.MaxDuration <= 0 {
		return nil, func() {}, nil
	}
	if opts.audioService == nil {
		return nil, nil, errors.New("audio service is required for session duration timing")
	}
	timer, err := opts.audioService.NewTimer(opts.clockSource, opts.MaxDuration)
	if err != nil {
		return nil, nil, err
	}
	return timer.C(), func() { timer.Stop() }, nil
}

func bindSessionLoopInputs(runCtx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions) error {
	if opts.loopReady != nil {
		select {
		case opts.loopReady <- loop:
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
	return nil
}
