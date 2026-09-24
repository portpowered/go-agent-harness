package runner

import (
	"context"
	"errors"
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

// RunWithResult executes one admitted bounded session and returns its
// service-owned terminal snapshot after cleanup.
func RunWithResult(service sessionduration.Service, request sessionduration.RunRequest, admitted sessionduration.AdmissionInferencer, publish func(sessionduration.Publication, messages.StreamMessage) error) (sessionduration.Result, error) {
	ctx := nonNilRunContext(request.Context)
	request = attachRunObserver(request)
	durationController, err := service.Begin(sessionduration.Options{
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
		service:             service,
		publish:             publish,
	}
	runner.state = runner.state.withAwaitingResponse(request.AudioInput.Run == nil)
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

func nonNilRunContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
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
	state                 runState
	updatedTimer          sessionduration.Timer
	updatedTimeout        <-chan time.Time
	finishOnce            sync.Once
	startOnce             sync.Once
	finished              bool
	finishErr             error
	result                sessionduration.Result
	service               sessionduration.Service
	publish               func(sessionduration.Publication, messages.StreamMessage) error
}

// runState contains decisions owned by one bounded invocation. It stays
// private to the runner so callers can supply observations and effects without
// replacing service policy or mutating the runner's state machine.
type runState struct {
	promptSent            bool
	closeSent             bool
	closeAfterOpenPending bool
	awaitingResponse      bool
}

func (s runState) hasPromptSent() bool { return s.promptSent }

func (s runState) hasCloseSent() bool { return s.closeSent }

func (s runState) hasCloseAfterOpenPending() bool { return s.closeAfterOpenPending }

func (s runState) isAwaitingResponse() bool { return s.awaitingResponse }

func (s runState) withPromptSent() runState {
	s.promptSent = true
	return s
}

func (s runState) withCloseSent(value bool) runState {
	s.closeSent = value
	return s
}

func (s runState) withCloseAfterOpenPending(value bool) runState {
	s.closeAfterOpenPending = value
	return s
}

func (s runState) withAwaitingResponse(value bool) runState {
	s.awaitingResponse = value
	return s
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
