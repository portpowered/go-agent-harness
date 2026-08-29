package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type roomParticipantRunResult struct {
	plan       *roomParticipantPlan
	runtime    *roomParticipantRuntime
	err        error
	connected  bool
	connectErr error
}

func defaultRoomSessionFactory(participant room.Participant, options SessionRunOptions) (messages.SessionInferencer, error) {
	inferencer, _, err := NewLiveSessionInferencer(options, participant.SystemPrompt)
	return inferencer, err
}

type roomParticipantDiagnosticSink struct {
	participantID string
	observer      RoomParticipantDiagnosticObserver
}

func (s roomParticipantDiagnosticSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if s.observer == nil {
		return
	}
	s.observer(s.participantID, record)
}

func runRoomParticipant(
	roomCtx context.Context,
	coordinator *roomCoordinator,
	runtime *roomParticipantRuntime,
	startGate <-chan struct{},
	opts RoomRunOptions,
	evidence *roomEvidence,
	results chan<- roomParticipantRunResult,
	runWG *sync.WaitGroup,
	mixerWG *sync.WaitGroup,
	secrets []string,
) {
	defer runWG.Done()
	defer func() {
		if runtime != nil && runtime.participantDone != nil {
			close(runtime.participantDone)
		}
	}()
	go func() {
		if mixerWG != nil {
			defer mixerWG.Done()
		}
		defer func() {
			if runtime != nil && runtime.mixerDone != nil {
				close(runtime.mixerDone)
			}
		}()
		pumpRoomMixer(roomCtx, coordinator, runtime, startGate, opts.OnAudioInput, secrets)
	}()

	participantEvidence := (*roomParticipantEvidence)(nil)
	if evidence != nil {
		participantEvidence = evidence.participant(runtime.plan.manifest.ID)
	}
	participantStream := RoomParticipantEventSink{}
	if opts.Stream != nil {
		participantStream = opts.Stream.ParticipantSink(runtime.plan.manifest.ID)
	}
	diagnosticSinks := roomParticipantDiagnosticSinks(runtime.plan, opts, participantEvidence, participantStream)
	observer := newSessionProgressObserver(combineRoomDiagnosticSinks(diagnosticSinks...), nil, runtime.plan.manifest.Provider, runtime.plan.manifest.Model)
	observer.turnAdmission = func(msg messages.StreamMessage) bool {
		value, ok := msg.Value.(*messages.MessageEndValue)
		if !ok || value == nil || value.TerminalReason == "" {
			return true
		}
		return value.TerminalReason == messages.TerminalReasonProviderAuthoredCompletion ||
			value.TerminalReason == messages.TerminalReasonLoopSynthesizedCompletion
	}
	observer.streamObserver = func(msg messages.StreamMessage) {
		observeRoomParticipantStream(coordinator, runtime, opts, evidence, participantEvidence, participantStream, msg)
	}
	observer.admittedTurnObserver = func(messages.StreamMessage) {
		turns := runtime.lifecycle.observeAdmittedTurn()
		coordinator.noteTurn(runtime.plan.manifest.ID, turns)
	}
	runErr := runAgentLoopSession(runtime.ctx, io.Discard, runtime.plan.tracker, sessionLoopOptions{
		Prompt:          runtime.plan.options.Prompt,
		WaitForClose:    true,
		Done:            coordinator.done,
		DoneErr:         coordinator.roomError,
		ToolExecutor:    runtime.plan.options.ToolExecutor,
		ToolDefinitions: cloneRoomToolDefinitions(runtime.plan.options.ToolDefinitions),
		observer:        observer,
		loopReady:       runtime.loopReady,
	})
	runtime.lifecycle.markRunDone(coordinator.participantRunError(runtime.plan.manifest.ID, runErr))
	connected, _, _, _, _, _, connectErr := runtime.lifecycle.snapshot()
	if trackedErr, ready := runtime.plan.tracker.outcome(); ready {
		connectErr = trackedErr
		runtime.lifecycle.markConnected(connectErr)
		connected = connectErr == nil
	}
	results <- roomParticipantRunResult{plan: runtime.plan, runtime: runtime, err: runErr, connected: connected, connectErr: connectErr}
}

func roomParticipantDiagnosticSinks(
	plan *roomParticipantPlan,
	opts RoomRunOptions,
	participantEvidence *roomParticipantEvidence,
	participantStream RoomParticipantEventSink,
) []SessionDiagnosticSink {
	diagnosticSinks := make([]SessionDiagnosticSink, 0, 2)
	if participantEvidence != nil {
		diagnosticSinks = append(diagnosticSinks, participantEvidence)
	}
	if opts.Stream != nil {
		diagnosticSinks = append(diagnosticSinks, participantStream)
	}
	if opts.OnDiagnostic != nil {
		diagnosticSinks = append(diagnosticSinks, roomParticipantDiagnosticSink{
			participantID: plan.manifest.ID,
			observer:      opts.OnDiagnostic,
		})
	}
	return diagnosticSinks
}

func observeRoomParticipantStream(
	coordinator *roomCoordinator,
	runtime *roomParticipantRuntime,
	opts RoomRunOptions,
	evidence *roomEvidence,
	participantEvidence *roomParticipantEvidence,
	participantStream RoomParticipantEventSink,
	msg messages.StreamMessage,
) {
	plan := runtime.plan
	if opts.onParticipantStream != nil {
		opts.onParticipantStream(plan.manifest.ID, msg)
	}
	if opts.Stream != nil {
		participantStream.ObserveStream(msg)
	}
	if participantEvidence != nil {
		if evidenceErr := participantEvidence.observeDelta(msg); evidenceErr != nil {
			evidence.recordError(plan.manifest.ID, fmt.Errorf("write stream delta: %w", evidenceErr))
		}
	}
	runtime.lifecycle.observe(msg)
	if msg.Type == messages.StreamTypeSessionOpen && opts.onParticipantSessionOpen != nil {
		opts.onParticipantSessionOpen(plan.manifest.ID)
	}
	if msg.Type != messages.StreamTypeAudioDelta || !assistantAudioDelta(msg) {
		return
	}
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok || value == nil {
		coordinator.fail(roomParticipantFailure(plan.manifest.ID, fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value), secretsForPlan(plan)))
		return
	}
	pcm := append([]byte(nil), value.Content...)
	if participantEvidence != nil {
		if evidenceErr := participantEvidence.observeAudio(pcm); evidenceErr != nil {
			evidence.recordError(plan.manifest.ID, fmt.Errorf("write WAV audio: %w", evidenceErr))
		}
	}
	if opts.OnAudioOutput != nil {
		if outputErr := opts.OnAudioOutput(plan.manifest.ID, append([]byte(nil), pcm...)); outputErr != nil {
			coordinator.fail(roomParticipantFailure(plan.manifest.ID, outputErr, secretsForPlan(plan)))
		}
	}
	for _, target := range coordinator.activeExcept(plan.manifest.ID) {
		if target == nil || target.mixer == nil {
			continue
		}
		if writeErr := target.mixer.WriteContext(runtime.ctx, plan.manifest.ID, pcm); writeErr != nil && coordinator.isActive(target.plan.manifest.ID) {
			coordinator.fail(roomParticipantFailure(plan.manifest.ID, fmt.Errorf("fan out PCM to %s: %w", target.plan.manifest.ID, writeErr), secretsForPlan(plan)))
		} else if opts.onParticipantAudioFanned != nil {
			opts.onParticipantAudioFanned(plan.manifest.ID, target.plan.manifest.ID, append([]byte(nil), pcm...))
		}
	}
}

func finalizeRoomParticipantResults(
	coordinator *roomCoordinator,
	plans []*roomParticipantPlan,
	secrets []string,
	cleanupErr error,
) (RoomTerminationReason, map[string]RoomParticipantResult, []string, error) {
	reason, participantResults, active, roomErr := coordinator.snapshot()
	if reason == "" {
		coordinator.fail(errors.New("room ended without a terminal reason"))
		reason, participantResults, active, roomErr = coordinator.snapshot()
	}
	completionErr := roomCompletionError(coordinator, plans)
	roomErr = errors.Join(roomErr, cleanupErr, completionErr)
	if completionErr != nil || cleanupErr != nil {
		reason = RoomTerminationFailed
	}
	for _, plan := range plans {
		if _, ok := participantResults[plan.manifest.ID]; ok {
			continue
		}
		connected, _, sessionClosed, closeReason, terminalReason, turns, connectErr := plan.participant.lifecycle.snapshot()
		participantReason := classifyRoomParticipantTermination(true, connectErr, connected, plan.participant.lifecycle.transportHasEnded(), sessionClosed, closeReason, terminalReason)
		participantResults[plan.manifest.ID] = RoomParticipantResult{
			ID:                plan.manifest.ID,
			ParticipantID:     plan.manifest.ID,
			TerminationReason: participantReason,
			Reason:            participantReason,
			TurnsCompleted:    turns,
			Connected:         connected,
			Error:             sanitizeRoomError(errors.Join(connectErr, completionErr), secretsForPlan(plan)),
		}
	}
	return reason, participantResults, sortedRoomIDs(active), roomErr
}

func roomCompletionError(coordinator *roomCoordinator, plans []*roomParticipantPlan) error {
	if coordinator == nil {
		return newRoomLifecycleWorkError("room coordinator")
	}
	_, results, active, _ := coordinator.snapshot()
	outstanding := make([]string, 0)
	for _, plan := range plans {
		if plan == nil {
			outstanding = append(outstanding, "participant plan")
			continue
		}
		id := plan.manifest.ID
		if _, exists := results[id]; !exists {
			outstanding = append(outstanding, roomLifecycleWorkLabel(id, "participant.terminal"))
		}
		outstanding = append(outstanding, roomParticipantOutstandingWork(plan.participant)...)
	}
	for _, id := range active {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "participant.terminal"))
	}
	return newRoomLifecycleWorkError(outstanding...)
}

func collectRoomParticipantResults(
	ctx context.Context,
	coordinator *roomCoordinator,
	plans []*roomParticipantPlan,
	mesh *room.Mesh,
	secrets []string,
	timer *time.Timer,
	results <-chan roomParticipantRunResult,
	cleanup *roomCleanupWaiter,
) error {
	pending := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		if plan != nil {
			pending[plan.manifest.ID] = struct{}{}
		}
	}
	if coordinator.isStopping() {
		cleanup.start()
	}
	ctxDone := ctx.Done()
	roomTimerDone := timerChannel(timer)
	roomDone := coordinator.done
	for len(pending) > 0 {
		select {
		case <-ctxDone:
			coordinator.stop(RoomTerminationStopped, nil)
			ctxDone = nil
			cleanup.start()
		case <-roomTimerDone:
			coordinator.stop(RoomTerminationMaxDurationReached, nil)
			roomTimerDone = nil
			cleanup.start()
		case <-roomDone:
			roomDone = nil
			cleanup.start()
		case <-cleanup.done():
			outstanding := make([]string, 0, len(pending))
			for id := range pending {
				outstanding = append(outstanding, roomLifecycleWorkLabel(id, "participant.terminal"))
			}
			for _, plan := range plans {
				outstanding = append(outstanding, roomParticipantOutstandingWork(plan.participant)...)
			}
			return newRoomLifecycleWorkError(outstanding...)
		case result := <-results:
			if result.plan == nil || result.runtime == nil {
				return newRoomLifecycleWorkError("participant result")
			}
			id := result.plan.manifest.ID
			if _, exists := pending[id]; !exists {
				coordinator.recordError(fmt.Errorf("duplicate room participant result %q", id))
				continue
			}
			delete(pending, id)
			if result.connectErr != nil && !coordinator.isStopping() {
				coordinator.fail(roomParticipantFailure(id, result.connectErr, secretsForPlan(result.plan)))
			}
			finishRoomParticipant(coordinator, mesh, result, secretsForPlan(result.plan), cleanup)
			if coordinator.isStopping() {
				cleanup.start()
			}
		}
	}
	return nil
}

func waitRoomParticipantWork(
	runWG *sync.WaitGroup,
	mixerWG *sync.WaitGroup,
	plans []*roomParticipantPlan,
	cleanup *roomCleanupWaiter,
) error {
	cleanup.start()
	done := make(chan struct{})
	go func() {
		if runWG != nil {
			runWG.Wait()
		}
		if mixerWG != nil {
			mixerWG.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-cleanup.done():
		outstanding := make([]string, 0)
		for _, plan := range plans {
			if plan != nil {
				outstanding = append(outstanding, roomParticipantOutstandingWork(plan.participant)...)
			}
		}
		return newRoomLifecycleWorkError(outstanding...)
	}
}

func closeRoomMeshBounded(mesh *room.Mesh, cleanup *roomCleanupWaiter) error {
	if mesh == nil {
		return nil
	}
	return boundedRoomCleanupOperation(cleanup, "mesh", mesh.Close)
}

func boundedRoomCleanupOperation(cleanup *roomCleanupWaiter, label string, operation func() error) error {
	if operation == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- operation() }()

	var timeout <-chan time.Time
	var timer *time.Timer
	if cleanup != nil && cleanup.timer != nil {
		timeout = cleanup.done()
	} else {
		timer = time.NewTimer(roomCleanupTimeout)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case err := <-done:
		return err
	case <-timeout:
		return newRoomLifecycleWorkError(label)
	}
}

func boundedRoomObserver(cleanup *roomCleanupWaiter, label string, observer func(), completed func()) error {
	return boundedRoomCleanupOperation(cleanup, label, func() error {
		observer()
		if completed != nil {
			completed()
		}
		return nil
	})
}

func cleanupRoomParticipantSetup(runtimes []*roomParticipantRuntime, mesh *room.Mesh, cleanup *roomCleanupWaiter) error {
	var cleanupErr error
	for _, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		if runtime.cancel != nil {
			runtime.cancel()
		}
		if runtime.mixer != nil {
			cleanupErr = errors.Join(cleanupErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(runtime.plan.manifest.ID, "mixer"), runtime.mixer.Close))
		}
	}
	if mesh != nil {
		cleanupErr = errors.Join(cleanupErr, closeRoomMeshBounded(mesh, cleanup))
	}
	return cleanupErr
}

func finishRoomParticipant(coordinator *roomCoordinator, mesh *room.Mesh, result roomParticipantRunResult, secrets []string, cleanup *roomCleanupWaiter) {
	if result.runtime == nil || result.plan == nil {
		return
	}
	if result.connectErr != nil {
		result.runtime.lifecycle.markConnected(result.connectErr)
	}
	roomStopping := coordinator.isStopping()
	_, _, sessionClosed, closeReason, terminalReason, _, _ := result.runtime.lifecycle.snapshot()
	reason := classifyRoomParticipantTermination(roomStopping, result.err, result.connected, result.runtime.lifecycle.transportHasEnded(), sessionClosed, closeReason, terminalReason)
	coordinator.finishParticipant(result.runtime, reason, result.err, secrets, mesh, cleanup)
}

func pumpRoomMixer(ctx context.Context, coordinator *roomCoordinator, runtime *roomParticipantRuntime, startGate <-chan struct{}, observer RoomParticipantAudioObserver, secrets []string) {
	if runtime == nil || runtime.mixer == nil {
		return
	}
	select {
	case <-startGate:
	case <-runtime.ctx.Done():
		return
	case <-ctx.Done():
		return
	}
	var loop *agentloop.AgentLoop
	select {
	case loop = <-runtime.loopReady:
	case <-runtime.ctx.Done():
		return
	case <-ctx.Done():
		return
	}
	if loop == nil {
		coordinator.fail(roomParticipantFailure(runtime.plan.manifest.ID, errors.New("room session loop did not become ready"), secretsForPlan(runtime.plan)))
		return
	}
	for {
		frame, err := runtime.mixer.ReadFrame(runtime.ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, room.ErrMixerClosed) || runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			coordinator.fail(roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("read inbound mixer: %w", err), secrets))
			return
		}
		if err := loop.SendAudioInput(runtime.ctx, frame); err != nil {
			if runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			coordinator.fail(roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("send mixed PCM: %w", err), secretsForPlan(runtime.plan)))
			return
		}
		if observer != nil {
			if err := observer(runtime.plan.manifest.ID, append([]byte(nil), frame...)); err != nil {
				coordinator.fail(roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("observe mixed PCM: %w", err), secretsForPlan(runtime.plan)))
				return
			}
		}
	}
}
