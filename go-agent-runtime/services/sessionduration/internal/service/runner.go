package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const (
	defaultLoopJoinTimeout       = 5 * time.Second
	defaultSessionUpdatedTimeout = 30 * time.Second
)

// Run is the single bounded-session loop boundary. Host-specific prompt,
// scheduled-input, and rendering behavior is supplied as a handler; deadline,
// admission, terminal publication, and cleanup remain service-owned.
func (s *Service) Run(request sessionduration.RunRequest) error {
	_, err := s.RunWithResult(request)
	return err
}

// RunWithResult executes one bounded session and returns the controller's
// service-owned terminal snapshot after cleanup.
func (s *Service) RunWithResult(request sessionduration.RunRequest) (sessionduration.Result, error) {
	ctx := nonNilRunContext(request.Context)
	request = attachRunObserver(request)
	if err := validateRunRequest(s, request); err != nil {
		return sessionduration.Result{}, err
	}
	admitted, err := runAdmission(request)
	if err != nil {
		return sessionduration.Result{}, err
	}
	deferAdmissionSessionClose(admitted)
	request.Close = closeAdmissionSessionAfterHost(admitted, request.Close)
	durationController, err := s.Begin(sessionduration.Options{
		Context:       ctx,
		Clock:         request.Clock,
		LivenessClock: request.LivenessClock,
		MaxDuration:   request.MaxDuration,
		DeferStart:    true,
		Liveness:      request.Liveness,
		Retry:         request.Retry,
		Terminal:      runTerminalSource(request.Terminal, admitted),
		Publication:   request.Publication,
		Artifacts:     request.Artifacts,
	})
	if err != nil {
		return sessionduration.Result{}, err
	}
	runCtx, cancel := newRunContext(ctx)
	loop, err := buildRunLoop(request, admitted, durationController, runCtx)
	if err != nil {
		admitted.CloseAdmission()
		cancel()
		result, finalizeErr := durationController.Finalize(ctx, sessionduration.FinalizeRequest{
			Primary:   err,
			Close:     request.Close,
			Binding:   request.Binding,
			Artifacts: request.Artifacts,
		})
		return result, finalizeErr
	}
	var audioInputWake chan struct{}
	if request.AudioInput.Run != nil {
		audioInputWake = make(chan struct{}, 1)
	}
	interruptionPending, interruptionWake, interruptionDone := startAudioInterruptionPump(runCtx, request.AudioInterruptions)
	wakeSources := append([]<-chan struct{}(nil), request.WakeSources...)
	if audioInputWake != nil {
		wakeSources = append(wakeSources, audioInputWake)
	}
	if interruptionWake != nil {
		wakeSources = append(wakeSources, interruptionWake)
	}
	request.WakeSources = wakeSources
	if request.ExternalErrorSources != nil {
		sources := append([]<-chan error(nil), request.ExternalErrorSources()...)
		request.ExternalErrors = mergeRunErrors(runCtx, request.ExternalErrors, sources...)
	}
	request.Wake = mergeRunWakes(runCtx, request.Wake, request.WakeSources...)
	request.Done = mergeRunDone(runCtx, request.Done, request.DoneSources...)
	runner := &runLoop{
		ctx:                 ctx,
		runCtx:              runCtx,
		cancel:              cancel,
		controller:          durationController,
		admitted:            admitted,
		loop:                loop,
		request:             request,
		audioInputWake:      audioInputWake,
		interruptionPending: interruptionPending,
		interruptionDone:    interruptionDone,
		runErrs:             make(chan error, 1),
		service:             s,
	}
	runner.state = runner.state.WithAwaitingResponse(request.AwaitingResponseOnCancel)
	runner.start()
	if err := durationController.Start(); err != nil {
		return runner.result, runner.finish(false, err)
	}
	err = runner.run()
	return runner.result, err
}

func attachRunObserver(request sessionduration.RunRequest) sessionduration.RunRequest {
	observer := request.Observer
	if observer == nil || !observer.Active() {
		if request.Facts.LastMessageEndAdmitted == nil {
			request.Facts.LastMessageEndAdmitted = func() bool { return true }
		}
		return request
	}
	request.Facts = observer.RunFacts()
	request.WakeSources = append(request.WakeSources, observer.ToolLifecycleEvents())
	request.SessionUpdated.Pending = observer.SessionUpdatedPending
	request.SessionUpdated.Ready = observer.SessionUpdatedReady
	request.Liveness.Enabled = true
	request.Retry.Enabled = true
	if request.Retry.MaxRetries <= 0 {
		request.Retry.MaxRetries = 1
	}
	if request.MaxDurationExpired == nil && !request.Policy.CloseAfterScheduledAudio {
		request.MaxDurationExpired = func() error { return observer.EnrichLifecycleError(nil) }
	}
	if request.RetryDispatched == nil {
		request.RetryDispatched = observer.RetryDispatched
	}
	if request.Effects.NoteUserTextInput == nil {
		request.Effects.NoteUserTextInput = observer.NoteUserTextInput
	}
	if request.Effects.DispatchScheduledInputs == nil {
		request.Effects.DispatchScheduledInputs = func(ctx context.Context, loop sessionduration.Loop) error {
			sender, ok := loop.(sessionduration.ScheduledInputSender)
			if !ok {
				return errors.New("session loop does not support scheduled audio input")
			}
			return observer.DispatchScheduledInputs(ctx, sender)
		}
	}
	priorWrite := request.Publication.Write
	request.Publication.Write = func(message messages.StreamMessage) error {
		observer.ObserveStreamMessage(message)
		if priorWrite != nil {
			return priorWrite(message)
		}
		return nil
	}
	return request
}

func startAudioInterruptionPump(ctx context.Context, port sessionduration.AudioInterruptionPort) (<-chan audioio.ScheduledAudioInput, <-chan struct{}, chan struct{}) {
	if port.Source == nil || port.Dispatch == nil {
		return nil, nil, nil
	}
	pending := make(chan audioio.ScheduledAudioInput, 1)
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case input, ok := <-port.Source:
				if !ok {
					return
				}
				select {
				case pending <- input:
					select {
					case wake <- struct{}{}:
					default:
					}
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return pending, wake, done
}

func mergeRunErrors(ctx context.Context, first <-chan error, rest ...<-chan error) <-chan error {
	sources := make([]<-chan error, 0, len(rest)+1)
	if first != nil {
		sources = append(sources, first)
	}
	for _, source := range rest {
		if source != nil {
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	if len(sources) == 1 {
		return sources[0]
	}
	merged := make(chan error, len(sources))
	for _, source := range sources {
		go func(source <-chan error) {
			for {
				select {
				case err, ok := <-source:
					if !ok {
						return
					}
					if err == nil {
						continue
					}
					select {
					case merged <- err:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(source)
	}
	return merged
}

func mergeRunWakes(ctx context.Context, first <-chan struct{}, rest ...<-chan struct{}) <-chan struct{} {
	sources := make([]<-chan struct{}, 0, len(rest)+1)
	if first != nil {
		sources = append(sources, first)
	}
	for _, source := range rest {
		if source != nil {
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	if len(sources) == 1 {
		return sources[0]
	}
	wake := make(chan struct{}, 1)
	for _, source := range sources {
		go func(source <-chan struct{}) {
			for {
				select {
				case _, ok := <-source:
					if !ok {
						return
					}
					select {
					case wake <- struct{}{}:
					default:
					}
				case <-ctx.Done():
					return
				}
			}
		}(source)
	}
	return wake
}

func mergeRunDone(ctx context.Context, first <-chan struct{}, rest ...<-chan struct{}) <-chan struct{} {
	sources := make([]<-chan struct{}, 0, len(rest)+1)
	if first != nil {
		sources = append(sources, first)
	}
	for _, source := range rest {
		if source != nil {
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	if len(sources) == 1 {
		return sources[0]
	}
	done := make(chan struct{})
	var once sync.Once
	for _, source := range sources {
		go func(source <-chan struct{}) {
			select {
			case <-source:
				once.Do(func() { close(done) })
			case <-ctx.Done():
			}
		}(source)
	}
	return done
}

type deferredAdmissionCloser interface {
	deferSessionCloseUntilFinalization()
	finalizeSessionClose() error
}

func deferAdmissionSessionClose(admitted sessionduration.AdmissionInferencer) {
	if closer, ok := admitted.(deferredAdmissionCloser); ok {
		closer.deferSessionCloseUntilFinalization()
	}
}

func closeAdmissionSessionAfterHost(admitted sessionduration.AdmissionInferencer, closeHost func() error) func() error {
	return func() error {
		var hostErr error
		if closeHost != nil {
			hostErr = closeHost()
		}
		if closer, ok := admitted.(deferredAdmissionCloser); ok {
			return errors.Join(hostErr, closer.finalizeSessionClose())
		}
		return hostErr
	}
}

func nonNilRunContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func validateRunRequest(s *Service, request sessionduration.RunRequest) error {
	if err := s.ValidateDuration(request.MaxDuration); err != nil {
		return err
	}
	if request.Inferencer == nil && request.Admission == nil {
		return errors.New("session duration inferencer is required")
	}
	if request.LoopFactory == nil {
		return errors.New("session duration loop factory is required")
	}
	return nil
}

func runAdmission(request sessionduration.RunRequest) (sessionduration.AdmissionInferencer, error) {
	if request.Admission != nil {
		return request.Admission, nil
	}
	return NewAdmissionInferencer(request.Inferencer, NewEventAdmission(), nil), nil
}

func runTerminalSource(source sessionduration.TerminalSource, admitted sessionduration.AdmissionInferencer) sessionduration.TerminalSource {
	if source.Message == nil {
		source.Message = admitted.ProviderTerminalMessage
	}
	if source.Matches == nil {
		source.Matches = admitted.IsProviderTerminalMessage
	}
	return source
}

func buildRunLoop(request sessionduration.RunRequest, admitted sessionduration.AdmissionInferencer, controller sessionduration.Controller, ctx context.Context) (sessionduration.Loop, error) {
	loop, err := request.LoopFactory(ctx, admitted, controller)
	if err != nil {
		return nil, err
	}
	if loop == nil || loop.Deltas() == nil {
		return nil, errors.New("session duration loop factory returned an invalid loop")
	}
	return loop, nil
}

func newRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

type runLoop struct {
	ctx                   context.Context
	runCtx                context.Context
	cancel                context.CancelFunc
	controller            sessionduration.Controller
	admitted              sessionduration.AdmissionInferencer
	loop                  sessionduration.Loop
	request               sessionduration.RunRequest
	audioInputWake        chan struct{}
	audioInputDone        chan struct{}
	audioInputErrs        chan error
	audioInputStop        context.CancelFunc
	audioInputReadHandled bool
	audioInputErr         error
	interruptionPending   <-chan audioio.ScheduledAudioInput
	interruptionDone      chan struct{}
	runErrs               chan error
	loopErr               error
	loopDone              bool
	pending               []messages.StreamMessage
	state                 sessionduration.RunState
	updatedTimer          sessionduration.Timer
	updatedTimeout        <-chan time.Time
	finishOnce            sync.Once
	startOnce             sync.Once
	finished              bool
	finishErr             error
	result                sessionduration.Result
	service               *Service
}

type runLoopEvent struct {
	kind  runLoopEventKind
	err   error
	msg   messages.StreamMessage
	valid bool
}

type runLoopEventKind uint8

const (
	runLoopControllerError runLoopEventKind = iota
	runLoopError
	runLoopExternalError
	runLoopWake
	runLoopDone
	runLoopSessionUpdatedTimeout
	runLoopMessage
	runLoopContext
)

func (r *runLoop) run() error {
	defer r.cancelRun()
	r.start()
	for {
		if result, done := r.handleEvent(r.nextEvent()); done {
			return result
		}
	}
}

func (r *runLoop) start() {
	r.startOnce.Do(func() {
		go func() { r.runErrs <- r.loop.Run(r.runCtx) }()
	})
}

func (r *runLoop) nextEvent() runLoopEvent {
	select {
	case err := <-r.controller.Errors():
		return runLoopEvent{kind: runLoopControllerError, err: err}
	case err := <-r.runErrs:
		return runLoopEvent{kind: runLoopError, err: err}
	case err := <-r.request.ExternalErrors:
		return runLoopEvent{kind: runLoopExternalError, err: err}
	case <-r.request.Wake:
		return runLoopEvent{kind: runLoopWake}
	case <-r.request.Done:
		return runLoopEvent{kind: runLoopDone}
	case <-r.updatedTimeout:
		return runLoopEvent{kind: runLoopSessionUpdatedTimeout, err: r.sessionUpdatedTimeoutError()}
	case msg, ok := <-r.loop.Deltas().Chan():
		return runLoopEvent{kind: runLoopMessage, msg: msg, valid: ok}
	case <-r.ctx.Done():
		return runLoopEvent{kind: runLoopContext, err: r.ctx.Err()}
	}
}

func (r *runLoop) handleEvent(event runLoopEvent) (error, bool) {
	switch event.kind {
	case runLoopControllerError:
		return r.finishControllerError(event.err), true
	case runLoopError:
		r.loopErr = event.err
		r.loopDone = true
		return r.finish(false, runLoopFailure(r.ctx, event.err)), true
	case runLoopExternalError:
		return r.finish(false, event.err), true
	case runLoopWake:
		return r.handleWake()
	case runLoopDone:
		return r.finish(false, runLoopDoneError(r.request)), true
	case runLoopSessionUpdatedTimeout:
		return r.finish(false, event.err), true
	case runLoopMessage:
		if !event.valid {
			return r.finish(false, nil), true
		}
		if err := r.process(event.msg); err != nil {
			return r.finish(false, err), true
		}
		if r.finished {
			return r.finishErr, true
		}
		return nil, false
	case runLoopContext:
		return r.finish(false, event.err), true
	default:
		return nil, false
	}
}

func (r *runLoop) handleWake() (error, bool) {
	inputResult := r.audioInputWakeResult()
	var audioInputErr error
	if r.request.Effects.OnWake != nil {
		result, err := r.request.Effects.OnWake(r.runCtx, r.loop)
		if inputResult.AudioInputCompleted {
			result.AudioInputCompleted = true
			result.AudioInputError = errors.Join(result.AudioInputError, inputResult.AudioInputError)
		}
		if result.AudioInputCompleted {
			r.state = r.state.WithAwaitingResponse(result.AudioInputError == nil)
		}
		audioInputErr = result.AudioInputError
		if err != nil {
			return r.finish(false, err), true
		}
	} else if r.request.OnWake != nil {
		state, err := r.request.OnWake(r.runCtx, r.loop, r.controller, r.state)
		r.state = state
		if err != nil {
			return r.finish(false, err), true
		}
		if inputResult.AudioInputCompleted {
			r.state = r.state.WithAwaitingResponse(inputResult.AudioInputError == nil)
			audioInputErr = inputResult.AudioInputError
		}
	} else if inputResult.AudioInputCompleted {
		r.state = r.state.WithAwaitingResponse(inputResult.AudioInputError == nil)
		audioInputErr = inputResult.AudioInputError
	}
	if dispatch := r.request.AudioInterruptions.Dispatch; dispatch != nil && r.interruptionPending != nil {
		select {
		case input := <-r.interruptionPending:
			if err := dispatch(r.runCtx, r.loop, input); err != nil {
				return r.finish(false, err), true
			}
		default:
		}
	}
	if audioInputErr != nil && !isAudioInputCancellation(audioInputErr) {
		return r.finish(false, audioInputErr), true
	}
	if r.request.Effects.DispatchScheduledInputs != nil {
		if err := r.request.Effects.DispatchScheduledInputs(r.runCtx, r.loop); err != nil {
			return r.finish(false, err), true
		}
	}
	state, err := r.closePendingSessionIfReady(r.runCtx, r.loop, r.state)
	r.state = state
	if err != nil {
		return r.finish(false, err), true
	}
	return nil, false
}

func runLoopDoneError(request sessionduration.RunRequest) error {
	if request.DoneError == nil {
		return nil
	}
	return request.DoneError()
}

func (r *runLoop) finishControllerError(err error) error {
	if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		return r.finishMaxDuration()
	}
	return r.finish(false, err)
}

func (r *runLoop) finishMaxDuration() error {
	var primary error
	if r.request.MaxDurationExpired != nil {
		primary = r.request.MaxDurationExpired()
	}
	return r.finish(true, primary)
}

func (r *runLoop) process(msg messages.StreamMessage) error {
	admission := r.controller.Observe(msg)
	if !admission.Accepted {
		r.pending = append(r.pending, msg)
		return nil
	}
	if err := publish(r.request.Publication, admission.Message); err != nil {
		return err
	}
	retryDispatched, err := r.retry(admission.Message)
	if err != nil {
		if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			return r.finishMaxDuration()
		}
		return err
	}
	if admission.Message.Type == messages.StreamTypeMessageEnd && !retryDispatched {
		r.state = r.state.WithAwaitingResponse(false)
	}
	if r.finished {
		return nil
	}
	if err := r.startSessionUpdatedTimer(admission.Message); err != nil {
		return err
	}
	var result sessionduration.MessageResult
	if r.request.Handle != nil {
		var err error
		result, err = r.request.Handle(r.runCtx, r.loop, r.controller, admission.Message, r.state)
		if result.State != nil {
			r.state = *result.State
		}
		if err != nil {
			if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
				return r.finishMaxDuration()
			}
			return err
		}
	} else {
		var err error
		result, err = r.handleSessionMessage(admission.Message)
		if result.State != nil {
			r.state = *result.State
		}
		if err != nil {
			if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
				return r.finishMaxDuration()
			}
			return err
		}
	}
	if r.request.SessionUpdated.Ready != nil && r.request.SessionUpdated.Ready() {
		r.stopSessionUpdatedTimer()
	}
	if !result.Stop {
		return nil
	}
	return r.finish(result.Planned, nil)
}

func (r *runLoop) handleSessionMessage(msg messages.StreamMessage) (sessionduration.MessageResult, error) {
	state := r.state
	policy := r.request.Policy
	facts := r.request.Facts
	effects := r.request.Effects

	if msg.Type == messages.StreamTypeSessionCreated && effects.SessionCreated != nil {
		if err := effects.SessionCreated(r.runCtx, r.loop); err != nil {
			return sessionduration.MessageResult{State: &state}, err
		}
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		if err := r.openSession(r.runCtx, r.loop, &state); err != nil {
			return sessionduration.MessageResult{State: &state}, err
		}
		if effects.SessionOpened != nil {
			if err := effects.SessionOpened(r.runCtx, r.loop); err != nil {
				return sessionduration.MessageResult{State: &state}, err
			}
		}
		if err := r.startAudioInput(r.runCtx, r.loop); err != nil {
			return sessionduration.MessageResult{State: &state}, err
		}
	}
	if shouldDispatchScheduledAudio(msg, policy.ScheduledAudioDispatch) && effects.DispatchScheduledInputs != nil {
		if err := effects.DispatchScheduledInputs(r.runCtx, r.loop); err != nil {
			return sessionduration.MessageResult{State: &state}, err
		}
	}

	if shouldQueueSessionClose(msg, policy, facts, state) {
		if !fact(facts.HasToolLifecycleObligation) {
			state = state.WithCloseSent(true).WithCloseAfterOpenPending(false).WithDrainPlayback()
			return sessionduration.MessageResult{Stop: true, Planned: true, State: &state}, nil
		}
		state = state.WithCloseAfterOpenPending(true)
	}
	if policy.HasAudioInput {
		if shouldStopAudioInputSession(msg, policy, facts, state) {
			state = state.WithDrainPlayback()
			return sessionduration.MessageResult{Stop: true, State: &state}, nil
		}
	} else if shouldStopSession(msg, policy, facts) {
		state = state.WithDrainPlayback()
		return sessionduration.MessageResult{Stop: true, State: &state}, nil
	}
	state, err := r.closePendingSessionIfReady(r.runCtx, r.loop, state)
	if err != nil {
		return sessionduration.MessageResult{State: &state}, err
	}
	return sessionduration.MessageResult{State: &state}, nil
}

func (r *runLoop) openSession(ctx context.Context, loop sessionduration.Loop, state *sessionduration.RunState) error {
	policy := r.request.Policy
	promptProvided := policy.PromptProvided || policy.Prompt != ""
	if promptProvided && !state.PromptSent() {
		*state = state.WithPromptSent()
		message := messages.NewTextMessage(messages.RoleUser, policy.Prompt)
		if err := loop.Send(ctx, []messages.Message{message}); err != nil {
			return fmt.Errorf("send session message: %w", err)
		}
		if r.request.Effects.NoteUserTextInput != nil {
			r.request.Effects.NoteUserTextInput(policy.Prompt)
		}
		if r.request.Effects.AwaitFirstTurn != nil {
			if err := r.request.Effects.AwaitFirstTurn(ctx); err != nil {
				return fmt.Errorf("send session first turn: %w", err)
			}
		}
	}
	if policy.CloseAfterOpen && !promptProvided && !policy.HasAudioInput && !state.CloseSent() {
		*state = state.WithCloseAfterOpenPending(true)
	}
	return nil
}

func (r *runLoop) closePendingSessionIfReady(ctx context.Context, loop sessionduration.Loop, state sessionduration.RunState) (sessionduration.RunState, error) {
	if state.CloseSent() || fact(r.request.Facts.HasToolLifecycleObligation) {
		return state, nil
	}
	closeAfterOpen := r.request.Policy.CloseAfterOpen && state.CloseAfterOpenPending()
	closeAfterScheduled := r.request.Policy.CloseAfterScheduledAudio && fact(r.request.Facts.ScheduledAudioComplete)
	if !closeAfterOpen && !closeAfterScheduled {
		return state, nil
	}
	message := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}
	if err := loop.Send(ctx, []messages.Message{message}); err != nil {
		return state, fmt.Errorf("close session loop: %w", err)
	}
	return state.WithCloseSent(true), nil
}

func shouldQueueSessionClose(msg messages.StreamMessage, policy sessionduration.RunPolicy, facts sessionduration.RunFacts, state sessionduration.RunState) bool {
	promptProvided := policy.PromptProvided || policy.Prompt != ""
	return policy.CloseAfterOpen && promptProvided && msg.Type == messages.StreamTypeMessageEnd &&
		fact(facts.LastMessageEndAdmitted) && !state.CloseSent()
}

func shouldStopSession(msg messages.StreamMessage, policy sessionduration.RunPolicy, facts sessionduration.RunFacts) bool {
	if isAuthoritativeSessionStop(msg) || hasTerminalRunFailure(msg, facts) {
		return true
	}
	if policy.CloseAfterOpen || policy.WaitForClose {
		return false
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if !fact(facts.LastMessageEndAdmitted) || fact(facts.HasToolLifecycleObligation) {
			return false
		}
		if policy.CloseAfterScheduledAudio && !fact(facts.ScheduledAudioComplete) {
			return false
		}
		return true
	case messages.StreamTypeTextEnd:
		return !fact(facts.HasToolLifecycleObligation)
	default:
		return false
	}
}

func shouldStopAudioInputSession(msg messages.StreamMessage, policy sessionduration.RunPolicy, facts sessionduration.RunFacts, state sessionduration.RunState) bool {
	if !state.AwaitingResponse() {
		return msg.Type == messages.StreamTypeSessionClose
	}
	if hasTerminalRunFailure(msg, facts) {
		return true
	}
	if policy.WaitForClose {
		return isRunTerminalErrorMessage(msg) || msg.Type == messages.StreamTypeSessionClose
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if !fact(facts.LastMessageEndAdmitted) {
			return false
		}
		if policy.RequireAssistantResponse && (msg.Role == messages.RoleTool || !fact(facts.AssistantResponseCompleted)) {
			return false
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return isRunTerminalErrorMessage(msg)
	}
}

func hasTerminalRunFailure(msg messages.StreamMessage, facts sessionduration.RunFacts) bool {
	return msg.Type == messages.StreamTypeMessageEnd &&
		(fact(facts.HasTerminalToolContinuationFailure) || fact(facts.HasTerminalScheduledResponseFailure))
}

func isAuthoritativeSessionStop(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeSessionClose || msg.Type == messages.StreamTypeLoopEnd || isRunTerminalErrorMessage(msg)
}

func isRunTerminalErrorMessage(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return ok && value != nil && !value.IsNonTerminal()
}

func shouldDispatchScheduledAudio(msg messages.StreamMessage, policy sessionduration.ScheduledAudioDispatch) bool {
	switch msg.Type {
	case messages.StreamTypeSessionOpen, messages.StreamTypeMessageEnd, messages.StreamTypeSessionUpdated:
		return true
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		return policy == sessionduration.ScheduledAudioActiveResponse
	default:
		return false
	}
}

func fact(read func() bool) bool {
	return read != nil && read()
}

func (r *runLoop) retry(msg messages.StreamMessage) (bool, error) {
	if msg.Type != messages.StreamTypeMessageEnd {
		return false, nil
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || terminal == nil {
		return false, nil
	}
	decision := r.controller.Retry(sessionduration.RetryRequest{Terminal: terminal})
	if !decision.Eligible {
		return false, nil
	}
	sender, ok := r.loop.(sessionduration.SessionEventSender)
	if !ok {
		return false, errors.New("session duration loop does not support provider session events")
	}
	if err := r.waitForRetry(decision.Delay); err != nil {
		return false, err
	}
	control := messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}
	if err := sender.SendSessionEvent(r.runCtx, control); err != nil {
		return false, fmt.Errorf("send rate-limit retry response: %w", err)
	}
	r.controller.ExpectProviderProgress()
	if r.request.RetryDispatched != nil {
		r.request.RetryDispatched(control)
	}
	return true, nil
}

func (r *runLoop) waitForRetry(delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if r.request.Clock == nil {
		return sessionduration.ErrSchedulerUnavailable
	}
	timer := r.request.Clock.NewTimer(delay)
	if timer == nil {
		return errors.New("session duration clock returned a nil retry timer")
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case err := <-r.controller.Errors():
		return err
	case <-r.request.Done:
		if err := runLoopDoneError(r.request); err != nil {
			return err
		}
		return context.Canceled
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-r.runCtx.Done():
		return r.runCtx.Err()
	}
}

func (r *runLoop) finish(planned bool, primary error) error {
	r.finishOnce.Do(func() {
		if errors.Is(primary, context.Canceled) && r.ctx.Err() != nil && r.state.AwaitingResponse() {
			primary = fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", r.ctx.Err())
		}
		r.stopSessionUpdatedTimer()
		r.admitted.CloseAdmission()
		drainPolicy := r.request.DrainPolicy
		if drainPolicy.Clock == nil {
			drainPolicy.Clock = r.request.Clock
		}
		pendingDrain := drainPolicy.Pending
		drainPolicy.Pending = func() bool {
			terminalToolFailure := r.request.Facts.HasTerminalToolContinuationFailure != nil && r.request.Facts.HasTerminalToolContinuationFailure()
			terminalScheduledFailure := r.request.Facts.HasTerminalScheduledResponseFailure != nil && r.request.Facts.HasTerminalScheduledResponseFailure()
			if terminalToolFailure || terminalScheduledFailure {
				return pendingDrain != nil && pendingDrain()
			}
			if !r.loopDone {
				select {
				case r.loopErr = <-r.runErrs:
					r.loopDone = true
				default:
					return true
				}
			}
			toolLifecyclePending := r.request.Facts.HasToolLifecycleObligation != nil && r.request.Facts.HasToolLifecycleObligation()
			if toolLifecyclePending && !terminalToolFailure && !terminalScheduledFailure {
				return true
			}
			return pendingDrain != nil && pendingDrain()
		}
		result, finalizeErr := r.controller.Finalize(r.ctx, sessionduration.FinalizeRequest{
			Primary: primary,
			Quiesce: func() error {
				r.quiesceAudioInput()
				if r.request.Quiesce != nil {
					return r.request.Quiesce()
				}
				return nil
			},
			Drain: func(ctx context.Context) error {
				drainErr := r.drainPending()
				if drainErr == nil && r.request.Drain != nil {
					drainErr = r.request.Drain(ctx, r.loop, r.controller, r.state)
				}
				if planned {
					drainErr = errors.Join(drainErr, sendLoopClose(r.runCtx, r.loop))
				}
				r.cancelRun()
				return drainErr
			},
			DrainLoop:   r.loop,
			DrainPolicy: drainPolicy,
			Close: func() error {
				closeErr := r.closeAudioInput()
				if r.interruptionDone != nil {
					<-r.interruptionDone
				}
				if r.request.Close != nil {
					closeErr = errors.Join(closeErr, r.request.Close())
				}
				joinCtx, cancel := context.WithTimeout(context.Background(), loopJoinTimeout(drainPolicy))
				defer cancel()
				loopErr := r.waitForLoop(joinCtx)
				if errors.Is(primary, loopErr) {
					loopErr = nil
				}
				return errors.Join(closeErr, loopErr)
			},
			Binding:   r.request.Binding,
			Artifacts: r.request.Artifacts,
		})
		r.result = result
		r.finishErr = errors.Join(finalizeErr, r.service.LifecycleError(sessionduration.LifecycleFailures{
			Runtime: r.admitted.RuntimeError(),
			Close:   r.admitted.CloseError(),
		}))
		r.finished = true
	})
	return r.finishErr
}

func (r *runLoop) startAudioInput(ctx context.Context, loop sessionduration.Loop) error {
	port := r.request.AudioInput
	if port.Run == nil || r.audioInputDone != nil {
		return nil
	}
	audioCtx, stop := context.WithCancel(ctx)
	r.audioInputStop = stop
	r.audioInputDone = make(chan struct{})
	r.audioInputErrs = make(chan error, 1)
	if port.BindContext != nil {
		port.BindContext(audioCtx)
	}
	go func() {
		err := port.Run(audioCtx, loop)
		r.audioInputErrs <- err
		close(r.audioInputDone)
		select {
		case r.audioInputWake <- struct{}{}:
		default:
		}
	}()
	return nil
}

func (r *runLoop) audioInputWakeResult() sessionduration.WakeResult {
	if r.audioInputErrs == nil || r.audioInputReadHandled {
		return sessionduration.WakeResult{}
	}
	select {
	case err := <-r.audioInputErrs:
		r.audioInputErr = err
		r.audioInputReadHandled = true
		return sessionduration.WakeResult{AudioInputCompleted: true, AudioInputError: err}
	default:
		return sessionduration.WakeResult{}
	}
}

func (r *runLoop) quiesceAudioInput() {
	if r.audioInputStop != nil {
		r.audioInputStop()
		r.audioInputStop = nil
	}
}

func (r *runLoop) closeAudioInput() error {
	r.quiesceAudioInput()
	if r.audioInputDone != nil {
		<-r.audioInputDone
	}
	if r.audioInputErrs != nil && !r.audioInputReadHandled {
		r.audioInputErr = <-r.audioInputErrs
		r.audioInputReadHandled = true
	}
	if isAudioInputCancellation(r.audioInputErr) {
		return nil
	}
	return r.audioInputErr
}

func isAudioInputCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (r *runLoop) startSessionUpdatedTimer(msg messages.StreamMessage) error {
	wait := r.request.SessionUpdated
	if msg.Type != messages.StreamTypeSessionOpen || wait.Pending == nil || !wait.Pending() || r.updatedTimer != nil {
		return nil
	}
	if r.request.Clock == nil {
		return sessionduration.ErrSchedulerUnavailable
	}
	timeout := wait.Timeout
	if timeout <= 0 {
		timeout = defaultSessionUpdatedTimeout
	}
	timer := r.request.Clock.NewTimer(timeout)
	if timer == nil {
		return errors.New("session duration clock returned a nil session-updated timer")
	}
	r.updatedTimer = timer
	r.updatedTimeout = timer.C()
	return nil
}

func (r *runLoop) stopSessionUpdatedTimer() {
	if r.updatedTimer == nil {
		return
	}
	r.updatedTimer.Stop()
	r.updatedTimer = nil
	r.updatedTimeout = nil
}

func (r *runLoop) sessionUpdatedTimeoutError() error {
	if err := r.request.SessionUpdated.TimeoutError; err != nil {
		return err
	}
	return errors.New("session updated acknowledgement timed out")
}

func loopJoinTimeout(policy sessionduration.DrainPolicy) time.Duration {
	if policy.LoopJoinTimeout <= 0 {
		return defaultLoopJoinTimeout
	}
	return policy.LoopJoinTimeout
}

func (r *runLoop) waitForLoop(ctx context.Context) error {
	if !r.loopDone {
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case r.loopErr = <-r.runErrs:
			r.loopDone = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if errors.Is(r.loopErr, context.Canceled) {
		return nil
	}
	return r.loopErr
}

func waitForLoop(results <-chan error) error {
	err := <-results
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (r *runLoop) cancelRun() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func sendLoopClose(ctx context.Context, loop sessionduration.Loop) error {
	if loop == nil {
		return nil
	}
	if err := loop.Send(ctx, []messages.Message{{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}}); err != nil {
		return fmt.Errorf("close session loop: %w", err)
	}
	return nil
}

func normalizeLoopError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil //nolint:nilerr // caller cancellation intentionally normalizes loop cancellation.
	}
	return err
}

func runLoopFailure(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return normalizeLoopError(ctx, err)
}

func (r *runLoop) drainPending() error {
	for _, msg := range r.pending {
		admission := r.controller.ObserveDrain(msg)
		if admission.Accepted {
			if err := publish(r.request.Publication, admission.Message); err != nil {
				return err
			}
		}
	}
	r.pending = nil
	return nil
}
