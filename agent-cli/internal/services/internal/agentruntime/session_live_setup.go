package agentruntime

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessionlivewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive/wire"
	"io"
)

func prepareSessionStreamOutput(out io.Writer, opts *sessionLoopOptions) (io.Writer, func(error) error) {
	if opts.terminalReporter != nil {
		return out, func(err error) error { return err }
	}
	reporter := newSessionTerminalReporter()
	opts.terminalReporter = reporter
	reporter.markRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	return renderer, func(runErr error) error {
		runErr = errors.Join(runErr, renderer.finishTranscript())
		return errors.Join(runErr, reporter.publish(out, runErr))
	}
}
func newObservedSessionLoop(inferencer messages.SessionInferencer, opts sessionLoopOptions) (*agentloop.AgentLoop, *observedSessionInferencer, <-chan error, error) {
	inferencer, pumpErrors := bindRTCDeviceSessionInferencer(inferencer, opts.rtcDeviceBinding)
	if err := ensureRTCDeviceBindingBuffers(opts.rtcDeviceBinding); err != nil {
		return nil, nil, nil, err
	}
	observed := newObservedSessionInferencer(inferencer, opts.runtime)
	observed.progress = opts.observer
	if opts.observer != nil {
		opts.observer.setLivenessClock(opts.livenessClock)
		opts.observer.setToolResultsEnabled(opts.ToolExecutor != nil)
	}
	loop, err := sessionlivewire.NewService().NewLoop(sessionLiveLoopOptions(observed, opts))
	if err != nil {
		return nil, nil, nil, err
	}
	return loop, observed, pumpErrors, nil
}
func bindSessionLoopInputs(runCtx, audioCtx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions) error {
	if opts.loopReady != nil {
		select {
		case opts.loopReady <- loop:
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
	if opts.AudioIn != nil {
		opts.AudioIn.bindContext(audioCtx)
	}
	return nil
}
