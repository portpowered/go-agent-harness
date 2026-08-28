package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func runRoomParticipant(
	roomCtx context.Context,
	coordinator *roomCoordinator,
	runtime *roomParticipantRuntime,
	startGate <-chan struct{},
	opts RoomRunOptions,
	evidence *roomEvidence,
	results chan<- roomParticipantRunResult,
	runWG *sync.WaitGroup,
	secrets []string,
) {
	defer runWG.Done()
	go pumpRoomMixer(roomCtx, coordinator, runtime, startGate, opts.OnAudioInput, secrets)

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
	observer.streamObserver = func(msg messages.StreamMessage) {
		observeRoomParticipantStream(coordinator, runtime, opts, evidence, participantEvidence, participantStream, msg)
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
	runtime.lifecycle.markRunDone()
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
	if opts.Stream != nil {
		participantStream.ObserveStream(msg)
	}
	if participantEvidence != nil {
		if evidenceErr := participantEvidence.observeDelta(msg); evidenceErr != nil {
			evidence.recordError(plan.manifest.ID, fmt.Errorf("write stream delta: %w", evidenceErr))
		}
	}
	turns := runtime.lifecycle.observe(msg)
	if msg.Type == messages.StreamTypeMessageEnd {
		coordinator.noteTurn(plan.manifest.ID, turns)
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
		}
	}
}

func finalizeRoomParticipantResults(
	coordinator *roomCoordinator,
	mesh *room.Mesh,
	plans []*roomParticipantPlan,
	secrets []string,
) (RoomTerminationReason, map[string]RoomParticipantResult, []string, error) {
	reason, participantResults, active, roomErr := coordinator.snapshot()
	if reason == "" {
		coordinator.fail(errors.New("room ended without a terminal reason"))
		reason, participantResults, active, roomErr = coordinator.snapshot()
	}
	if meshErr := mesh.Close(); meshErr != nil {
		roomErr = errors.Join(roomErr, meshErr)
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
			Error:             sanitizeRoomError(connectErr, secretsForPlan(plan)),
		}
	}
	return reason, participantResults, sortedRoomIDs(active), roomErr
}
