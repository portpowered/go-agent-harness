package durationrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type durationRunner struct {
	durationService sessionduration.Service
	loopFactory     sessionduration.DuplexLoopFactory
}

func NewDurationRunner(durationService sessionduration.Service, loopFactory sessionduration.DuplexLoopFactory) session.DurationRunner {
	return &durationRunner{durationService: durationService, loopFactory: loopFactory}
}

func (r *durationRunner) RunDuration(request session.DurationRunRequest) (sessionduration.Result, error) {
	if err := r.validate(); err != nil {
		return sessionduration.Result{}, err
	}
	run := r.newRunRequest(request)
	resources := r.newResources(request, run)
	r.configureRun(&run, resources)
	result, err := r.durationService.RunWithResult(run)
	return r.complete(request, result, err)
}

func (r *durationRunner) validate() error {
	if r == nil || r.durationService == nil {
		return errors.New("session duration service is required")
	}
	if r.loopFactory == nil {
		return errors.New("session loop factory is required")
	}
	return nil
}

func (r *durationRunner) newRunRequest(request session.DurationRunRequest) sessionduration.RunRequest {
	clock := request.Clock
	if clock == nil {
		clock = platformclock.Real{}
	}
	livenessClock := request.LivenessClock
	if livenessClock == nil {
		livenessClock = platformclock.Real{}
	}
	dispatch := request.ScheduledAudioDispatch
	if dispatch == "" {
		dispatch = sessionduration.ScheduledAudioCompletionGated
	}
	completionObserver := request.CompletionObserver
	if completionObserver == nil {
		completionObserver, _ = request.Observer.(sessionduration.CompletionObserver)
	}
	run := sessionduration.RunRequest{
		Context:            request.Context,
		Inferencer:         request.Inferencer,
		Admission:          request.Admission,
		Clock:              clock,
		LivenessClock:      livenessClock,
		MaxDuration:        request.MaxDuration,
		AudioInput:         request.AudioInput,
		AudioInterruptions: request.AudioInterruptions,
		Observer:           request.Observer,
		SessionUpdated: sessionduration.SessionUpdatedWait{
			Timeout:      request.SessionUpdatedTimeout,
			TimeoutError: request.SessionUpdatedTimeoutError,
		},
		Effects:     request.Effects,
		Publication: request.Publication,
		Done:        request.Done,
		DoneError:   request.DoneError,
		Policy: sessionduration.RunPolicy{
			Prompt:                           request.Prompt,
			PromptProvided:                   request.PromptProvided,
			CloseAfterOpen:                   request.CloseAfterOpen,
			WaitForClose:                     request.WaitForClose,
			HasAudioInput:                    request.AudioInput.Run != nil,
			RequireAssistantResponse:         request.RequireAssistantResponse,
			RequireTerminalAssistantResponse: request.RequireTerminalReply,
			CloseAfterScheduledAudio:         request.CloseAfterScheduledAudio,
			ScheduledAudioDispatch:           dispatch,
		},
	}
	return run
}

func (r *durationRunner) newResources(request session.DurationRunRequest, run sessionduration.RunRequest) *durationResources {
	return &durationResources{
		request:     request,
		run:         run,
		loopOptions: durationLoopOptions(request),
		service:     r.durationService,
		loopFactory: r.loopFactory,
		done:        make(chan struct{}),
	}
}

func (r *durationRunner) configureRun(run *sessionduration.RunRequest, resources *durationResources) {
	run.LoopFactory = resources.buildLoop
	run.Quiesce = resources.quiesce
	run.Drain = resources.drain
	run.Close = resources.close
	run.Binding = resources.closeBinding
	priorErrorSources := run.ExternalErrorSources
	run.ExternalErrorSources = func() []<-chan error {
		sources := resources.errorSources()
		if priorErrorSources != nil {
			sources = append(sources, priorErrorSources()...)
		}
		return sources
	}
	run.DoneSources = append(append([]<-chan struct{}(nil), run.DoneSources...), resources.done)
	priorSessionCreated := run.Effects.SessionCreated
	run.Effects.SessionCreated = func(ctx context.Context, loop sessionduration.Loop) error {
		if resources.publisher != nil {
			resources.publisher.MarkReady()
		}
		if priorSessionCreated != nil {
			return priorSessionCreated(ctx, loop)
		}
		return nil
	}
}

func (r *durationRunner) complete(request session.DurationRunRequest, result sessionduration.Result, runErr error) (sessionduration.Result, error) {
	durationExpired := result.Expired && request.CloseAfterScheduledAudio && request.Observer != nil
	completionObserver := request.CompletionObserver
	if completionObserver == nil {
		completionObserver, _ = request.Observer.(sessionduration.CompletionObserver)
	}
	runErr = r.durationService.Complete(sessionduration.CompletionRequest{
		RunError:                      runErr,
		AudioOutputError:              request.Completion.AudioOutputError,
		AssistantResponseIncomplete:   request.Completion.AssistantResponseIncomplete,
		ScheduledAudioIncompleteCause: request.Completion.ScheduledAudioIncompleteCause,
		HasAudioInput:                 request.AudioInput.Run != nil,
		RequireAssistantResponse:      request.RequireAssistantResponse,
		RequireTerminalAssistantReply: request.RequireTerminalReply,
		BoundCancellation:             request.Completion.BoundCancellation,
		DurationExpired:               durationExpired,
		CloseAfterScheduledAudio:      request.CloseAfterScheduledAudio,
		Observer:                      completionObserver,
	})
	userCancelled := false
	if request.FinishObserver != nil {
		runErr, userCancelled = request.FinishObserver(runErr, durationExpired)
	}
	if userCancelled && request.PublishUserCancellation != nil {
		runErr = errors.Join(runErr, request.PublishUserCancellation())
	}
	return result, runErr
}

func durationLoopOptions(request session.DurationRunRequest) sessionduration.DuplexLoopOptions {
	options := sessionduration.DuplexLoopOptions{
		ToolExecutor:             request.ToolExecutor,
		ToolDefinitions:          append([]messages.ToolDefinition(nil), request.ToolDefinitions...),
		AdvertiseToolDefinitions: request.AdvertiseToolDefinitions,
	}
	policy := request.InteractiveToolPolicy
	if policy == nil {
		return options
	}
	longRunning := make([]string, 0, len(request.ToolDefinitions))
	for _, definition := range request.ToolDefinitions {
		if policy.ClassForTool(definition.Name) == runtimeTools.InteractiveToolClassBoundedLongRunning {
			longRunning = append(longRunning, definition.Name)
		}
	}
	settings := policy.Settings()
	options.ToolAcknowledgementPolicy = &sessionduration.DuplexToolAcknowledgementPolicy{
		Threshold:            settings.AcknowledgementThreshold,
		LongRunningToolNames: longRunning,
	}
	return options
}

type durationResources struct {
	request        session.DurationRunRequest
	run            sessionduration.RunRequest
	loopOptions    sessionduration.DuplexLoopOptions
	service        sessionduration.Service
	loopFactory    sessionduration.DuplexLoopFactory
	lifecycle      session.DurationSessionLifecycle
	publisher      session.DurationPublication
	publisherError <-chan error
	bindingError   <-chan error
	done           chan struct{}
}

func (r *durationResources) buildLoop(ctx context.Context, admitted sessionduration.AdmissionInferencer, controller sessionduration.Controller) (sessionduration.Loop, error) {
	inferencer, err := r.prepareInferencer(admitted, controller)
	if err != nil {
		return nil, err
	}
	loop, err := r.loopFactory.Build(ctx, inferencer, r.buildOptions())
	if err != nil {
		return nil, fmt.Errorf("create session agent loop: %w", err)
	}
	if err := r.startLoopResources(ctx, loop); err != nil {
		return nil, err
	}
	return loop, nil
}

func (r *durationResources) prepareInferencer(admitted sessionduration.AdmissionInferencer, controller sessionduration.Controller) (messages.SessionInferencer, error) {
	if observer := r.run.Observer; observer != nil {
		observer.SetToolResultsEnabled(r.loopOptions.ToolExecutor != nil)
		observer.SetDurationController(controller)
	}
	inferencer := messages.SessionInferencer(admitted)
	if r.request.WrapInferencer != nil {
		wrapped, lifecycle, err := r.request.WrapInferencer(admitted)
		if err != nil {
			return nil, err
		}
		inferencer = wrapped
		r.lifecycle = lifecycle
	}
	if r.request.ControllerReady != nil {
		r.request.ControllerReady(controller)
	}
	return inferencer, nil
}

func (r *durationResources) buildOptions() sessionduration.DuplexLoopOptions {
	options := r.loopOptions
	options.ToolDefinitions = append([]messages.ToolDefinition(nil), options.ToolDefinitions...)
	if binding, ok := r.request.Binding.(devices.RTCBindingAudioPorts); ok {
		options.AudioPorts = binding.AudioPorts()
	}
	return options
}

func (r *durationResources) startLoopResources(ctx context.Context, loop sessionduration.Loop) error {
	r.bindingError = bindingErrors(r.request.Binding)
	if err := r.startPublication(ctx, loop); err != nil {
		return err
	}
	if r.lifecycle != nil && r.lifecycle.Done() != nil {
		go func() {
			select {
			case <-r.lifecycle.Done():
				close(r.done)
			case <-ctx.Done():
			}
		}()
	}
	if r.request.LoopReady != nil {
		if err := r.request.LoopReady(loop); err != nil {
			if r.publisher != nil {
				r.publisher.Stop()
			}
			return err
		}
	}
	return nil
}

func (r *durationResources) startPublication(ctx context.Context, loop sessionduration.Loop) error {
	if r.request.StartPublication == nil {
		return nil
	}
	publisher, err := r.request.StartPublication(ctx, loop)
	r.publisher = publisher
	if err != nil {
		return err
	}
	if r.publisher != nil {
		r.publisherError = r.publisher.Errors()
	}
	return nil
}

func bindingErrors(binding devices.RTCBinding) <-chan error {
	if binding == nil {
		return nil
	}
	return binding.Errors()
}

func (r *durationResources) quiesce() error {
	if r == nil || r.request.QuiesceUpstream == nil {
		return nil
	}
	return r.request.QuiesceUpstream()
}

func (r *durationResources) drain(ctx context.Context) error {
	if r == nil || r.lifecycle == nil {
		return nil
	}
	return r.lifecycle.DrainPlayback(ctx)
}

func (r *durationResources) close() error {
	if r == nil {
		return nil
	}
	if r.publisher != nil {
		r.publisher.Stop()
	}
	var result error
	if r.request.CloseProviderOnShutdown && r.lifecycle != nil {
		result = errors.Join(result, r.lifecycle.Close())
	}
	if r.lifecycle != nil {
		result = errors.Join(result, r.service.TransportError(r.lifecycle.Error()))
	}
	return result
}

func (r *durationResources) closeBinding() error {
	if r == nil || r.request.Binding == nil {
		return nil
	}
	return r.request.Binding.Close()
}

func (r *durationResources) errorSources() []<-chan error {
	if r == nil {
		return nil
	}
	return []<-chan error{r.publisherError, r.bindingError}
}
