package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntimeContext(ctx, opts)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// sessionInstructionsInferencer decorates caller-owned session seams without
// changing their provider construction. The provider-aware runtime factory
// handles the live provider path; injected sessions receive a generic session
// update after the provider announces that the session is open.
type sessionInstructionsInferencer struct {
	inner        messages.SessionInferencer
	instructions string
	tools        []messages.ToolDefinition
}

var _ messages.SessionInferencer = (*sessionInstructionsInferencer)(nil)

func newSessionInstructionsInferencer(inner messages.SessionInferencer, instructions string, toolDefinitions []messages.ToolDefinition) messages.SessionInferencer {
	return &sessionInstructionsInferencer{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
	}
}

func cloneSessionToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func (i *sessionInstructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newSessionInstructionsSession(inner, ctx, i.instructions, i.tools), nil
}

type sessionInstructionsSession struct {
	inner         messages.Session
	instructions  string
	tools         []messages.ToolDefinition
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	configureOnce sync.Once
	done          chan struct{}
	doneOnce      sync.Once
}

var _ messages.Session = (*sessionInstructionsSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionInstructionsSession)(nil)

func newSessionInstructionsSession(inner messages.Session, parent context.Context, instructions string, toolDefinitions []messages.ToolDefinition) messages.Session {
	ctx, cancel := context.WithCancel(parent)
	session := &sessionInstructionsSession{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
		receive:      messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go session.relay()
	return session
}

func (s *sessionInstructionsSession) relay() {
	defer s.markDone()
	innerReceive := s.inner.Receive()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.inner.Done():
			s.drainAfterDone(innerReceive)
			return
		case msg := <-innerReceive.Chan():
			if !s.forward(msg) {
				return
			}
		}
	}
}

func (s *sessionInstructionsSession) drainAfterDone(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := innerReceive.Read()
		if !ok || !s.forward(msg) {
			return
		}
	}
}

func (s *sessionInstructionsSession) forward(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeSessionCreated {
		var configureErr error
		s.configureOnce.Do(func() {
			outcome := messages.SendSessionWithOutcome(s.ctx, s.inner, messages.StreamMessage{
				Type: messages.StreamTypeSessionUpdate,
				Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
					Instructions: s.instructions,
					Tools:        s.tools,
				}),
			})
			if !outcome.OK() {
				configureErr = fmt.Errorf("send session instructions: %s", outcome.Status)
				if outcome.Err != nil {
					configureErr = fmt.Errorf("%w: %w", configureErr, outcome.Err)
				}
			}
		})
		if configureErr != nil {
			if closeErr := s.inner.Close(); closeErr != nil {
				configureErr = errors.Join(configureErr, fmt.Errorf("close session after instruction failure: %w", closeErr))
			}
			s.receive.Write(s.ctx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(configureErr),
			})
			return false
		}
	}
	return s.receive.Write(s.ctx, msg)
}

func (s *sessionInstructionsSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

// RequestResponse forwards the optional explicit response capability without
// changing the instruction-update lifecycle or replay behavior.
func (s *sessionInstructionsSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *sessionInstructionsSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

// SendMessage forwards the optional complete-message capability of the
// wrapped provider session. Instruction decoration must not hide the rich
// message path used to deliver a tool result on the next model turn.
func (s *sessionInstructionsSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards deferred complete messages for callers
// that batch more than one tool result before requesting the next response.
func (s *sessionInstructionsSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionInstructionsSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *sessionInstructionsSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *sessionInstructionsSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *sessionInstructionsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionInstructionsSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionInstructionsSession) rtcMedia() (audio.MediaEndpoints, bool) {
	return sessionMediaFromSession(s.inner)
}

func (s *sessionInstructionsSession) RTCMedia() audio.MediaEndpoints {
	media, _ := s.rtcMedia()
	return media
}

func (s *sessionInstructionsSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

func (s *sessionInstructionsSession) Close() error {
	s.cancel()
	err := s.inner.Close()
	s.markDone()
	return err
}

func (s *sessionInstructionsSession) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

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
	publisher    *sessionDynamicToolPublisher
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
	if err == nil && msg.Type == messages.StreamTypeSessionCreated {
		// SESSION.UPDATE is sent while handling SESSION.CREATED. Release
		// dynamic publication only after that bootstrap boundary.
		r.publisher.markSessionReady()
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
	r.publisher.stop()
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
