package agentruntime

import (
	"context"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// runBoundedSessionStream delegates an explicit --max-duration run to the
// sessionduration service. This host supplies loop construction, rendering,
// and the shared session reactions; the service owns admission, the deadline,
// retry timing, terminal publication, drain, and loop shutdown.
func runBoundedSessionStream(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	service := durationwire.NewService()
	admitted, ok := inferencer.(sessionduration.AdmissionInferencer)
	if !ok {
		admitted = service.NewAdmissionInferencer(inferencer, service.NewEventAdmission(), nil)
	}
	run := &boundedSessionRun{opts: opts, admitted: admitted}
	defer run.stop()
	request := sessionduration.RunRequest{
		Context: ctx, Admission: admitted, Clock: opts.durationClock, MaxDuration: opts.durationBound,
		Publication: sessionduration.Publication{Artifacts: service.ArtifactsFromContext(ctx), Write: publishSessionMessage(out, opts)},
		LoopFactory: run.buildLoop, Handle: run.handle, DoneError: opts.DoneErr,
		ExternalErrorSources: func() []<-chan error { return run.errorSources },
		DoneSources:          func() []<-chan struct{} { return []<-chan struct{}{opts.Done, run.observed.Done()} },
		Close:                func() error { return closeBareSessionIfNeeded(opts.BareLive, run.observed) },
		AwaitAdmissionClose:  true,
		Completion:           run.complete,
	}
	if observer := opts.observer; observer != nil {
		request.Wake, request.OnWake = observer.ToolLifecycleEvents(), run.wake
		request.RetryClaim, request.RetryDispatched = observer.ClaimScheduledRateLimitRetry, observer.ObserveProviderDispatch
		if opts.RequireSessionUpdated {
			timeout := opts.SessionUpdatedTimeout
			if timeout <= 0 {
				timeout = sessionScheduledAudioConfigTimeout
			}
			request.SessionUpdated = sessionduration.SessionUpdatedWait{Timeout: timeout, Pending: observer.ScheduledAudioAwaitingConfiguration, Ready: observer.ScheduledAudioReady, TimeoutError: sessionScheduledAudioConfigTimeoutError(opts)}
		}
	}
	err := service.Run(request)
	if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
		// A caller cancellation that races a clean loop or provider completion
		// must not be erased by the terminal path that won the race.
		err = errors.Join(err, ctxErr)
	}
	return err
}

// boundedSessionRun holds the host resources of one bounded session: the
// constructed loop, its provider observation, and dynamic tool publication.
type boundedSessionRun struct {
	opts         sessionLoopOptions
	admitted     sessionduration.AdmissionInferencer
	loop         *agentloop.AgentLoop
	observed     *observedSessionInferencer
	publisher    sessionturn.Publication
	errorSources []<-chan error
	state        sessionLoopMessageState
}

func (r *boundedSessionRun) buildLoop(runCtx context.Context, _ sessionduration.AdmissionInferencer, _ sessionduration.Controller) (sessionduration.Loop, error) {
	loop, observed, deviceErrors, err := newObservedSessionLoop(r.admitted, r.opts)
	if err != nil {
		return nil, err
	}
	r.loop, r.observed = loop, observed
	var publisherErrors <-chan error
	r.publisher, publisherErrors = startSessionDynamicToolPublisher(runCtx, loop, r.opts)
	r.errorSources = []<-chan error{publisherErrors, deviceErrors}
	if r.opts.observer != nil {
		r.errorSources = append(r.errorSources, r.opts.observer.LivenessErrors(runCtx))
	}
	if err := bindSessionLoopInputs(runCtx, loop, r.opts); err != nil {
		return nil, err
	}
	return loop, nil
}

func (r *boundedSessionRun) handle(runCtx context.Context, _ sessionduration.Loop, _ sessionduration.Controller, msg messages.StreamMessage) (sessionduration.MessageResult, error) {
	var stop bool
	var err error
	r.state, stop, err = reactToSessionLoopMessage(runCtx, r.loop, r.opts, msg, r.state)
	if err == nil && msg.Type == messages.StreamTypeSessionCreated && r.publisher != nil {
		// SESSION.UPDATE is sent while handling SESSION.CREATED. Release
		// dynamic publication only after that bootstrap boundary.
		r.publisher.MarkSessionReady()
	}
	return sessionduration.MessageResult{Stop: stop}, err
}

// wake re-runs scheduled input and close decisions after an asynchronous tool
// lifecycle transition that produced no provider delta.
func (r *boundedSessionRun) wake(runCtx context.Context, _ sessionduration.Loop, _ sessionduration.Controller) error {
	if err := r.opts.observer.DispatchScheduledInputs(runCtx, r.loop); err != nil {
		return err
	}
	var err error
	r.state, err = closePendingSessionIfReady(runCtx, r.loop, r.opts, r.state)
	return err
}

func (r *boundedSessionRun) complete(result sessionduration.Result, runErr error) error {
	if r.opts.terminalReporter != nil {
		r.opts.terminalReporter.MarkDurationExpiry(result.Expired, result.OutputState)
	}
	var transportErr error
	if r.observed != nil {
		transportErr = durationwire.NewService().TransportError(r.observed.sessionFailure())
	}
	if r.opts.durationCompletionPublisher == nil {
		return transportErr
	}
	return errors.Join(transportErr, r.opts.durationCompletionPublisher(result.Expired, errors.Join(runErr, transportErr)))
}

func (r *boundedSessionRun) stop() {
	if r.opts.observer != nil {
		r.opts.observer.StopLiveness()
	}
	if r.publisher != nil {
		r.publisher.Stop()
	}
}

// sessionArtifactWriter records a host-synthesized terminal in the bounded
// run's duration artifacts before rendering it.
func sessionArtifactWriter(ctx context.Context) func(io.Writer, messages.StreamMessage) error {
	service := durationwire.NewService()
	artifacts := service.ArtifactsFromContext(ctx)
	return func(out io.Writer, msg messages.StreamMessage) error {
		if artifacts != nil {
			if err := artifacts.Accept(msg); err != nil {
				return wrapSessionPhaseError("write duration artifacts", err)
			}
		}
		return service.WriteMessage(out, msg)
	}
}

// boundCancellationClosed reports whether the owner-initiated bound
// cancellation channel has fired. A nil channel never fires.
func boundCancellationClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
