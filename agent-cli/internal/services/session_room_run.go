package services

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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
		if roomParticipantIsHuman(runtime.plan) {
			pumpRoomHumanOutput(roomCtx, coordinator, runtime, startGate, secrets)
			return
		}
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
	if roomParticipantIsHuman(runtime.plan) {
		runErr := runRoomHumanCapture(roomCtx, coordinator, runtime, startGate, participantEvidence, opts, secrets)
		runErr = coordinator.participantRunError(runtime.plan.manifest.ID, runErr)
		runtime.lifecycle.markRunDone(runErr)
		connected, _, _, _, _, _, connectErr := runtime.lifecycle.snapshot()
		results <- roomParticipantRunResult{plan: runtime.plan, runtime: runtime, err: runErr, connected: connected, connectErr: connectErr}
		return
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
		Prompt:                 runtime.plan.options.Prompt,
		WaitForClose:           true,
		Done:                   coordinator.done,
		DoneErr:                coordinator.roomError,
		ToolExecutor:           runtime.plan.options.ToolExecutor,
		ToolDefinitions:        cloneRoomToolDefinitions(runtime.plan.options.ToolDefinitions),
		ToolDefinitionBase:     cloneRoomToolDefinitions(runtime.plan.options.ToolDefinitionBase),
		RefreshToolDefinitions: runtime.plan.options.RefreshToolDefinitions,
		BrowserWatch:           runtime.plan.options.BrowserWatch,
		observer:               observer,
		loopReady:              runtime.loopReady,
	})
	if closeErr := closeRoomParticipantCapability(runtime.plan); closeErr != nil {
		runErr = errors.Join(runErr, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("close browser tools: %w", closeErr), secretsForPlan(runtime.plan)))
	}
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
		var closeErr error
		for _, plan := range plans {
			if plan == nil || plan.participant == nil || roomParticipantIsHuman(plan) || plan.participant.lifecycle == nil {
				continue
			}
			closeErr = errors.Join(closeErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(plan.manifest.ID, "session.close"), plan.participant.lifecycle.closeOwnedSession))
		}
		return closeErr
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
		cleanupErr = errors.Join(cleanupErr, closeRoomParticipantDevices(runtime, cleanup))
		if runtime.mixer != nil {
			cleanupErr = errors.Join(cleanupErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(runtime.plan.manifest.ID, "mixer"), runtime.mixer.Close))
		}
	}
	if mesh != nil {
		cleanupErr = errors.Join(cleanupErr, closeRoomMeshBounded(mesh, cleanup))
	}
	return cleanupErr
}

func closeRoomParticipantDevices(runtime *roomParticipantRuntime, cleanup *roomCleanupWaiter) error {
	if runtime == nil || runtime.plan == nil {
		return nil
	}
	id := runtime.plan.manifest.ID
	var closeErr error
	if runtime.input != nil {
		closeErr = errors.Join(closeErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "input.device"), runtime.input.Close))
	}
	if runtime.output != nil {
		closeErr = errors.Join(closeErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "output.device"), runtime.output.Close))
	}
	return closeErr
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

func runRoomHumanCapture(
	roomCtx context.Context,
	coordinator *roomCoordinator,
	runtime *roomParticipantRuntime,
	startGate <-chan struct{},
	participantEvidence *roomParticipantEvidence,
	opts RoomRunOptions,
	secrets []string,
) error {
	if runtime == nil || runtime.plan == nil || runtime.input == nil || runtime.mixer == nil {
		return errors.New("human participant input device is not ready")
	}
	select {
	case <-startGate:
	case <-runtime.ctx.Done():
		return nil
	case <-roomCtx.Done():
		return nil
	}

	participantID := runtime.plan.manifest.ID
	frame := make([]int16, audio.FrameSize)
	for {
		if err := runtime.input.ReadFrame(runtime.ctx, frame); err != nil {
			if errors.Is(err, io.EOF) || runtime.ctx.Err() != nil || coordinator.isStopping() || errors.Is(err, context.Canceled) {
				return nil
			}
			failure := roomParticipantFailure(participantID, fmt.Errorf("read human input device: %w", err), secrets)
			coordinator.fail(failure)
			return failure
		}
		roomSamples, err := resampleRoomSamples(frame, audio.SampleRate, runtime.mixer.Format().SampleRate)
		if err != nil {
			failure := roomParticipantFailure(participantID, fmt.Errorf("convert human input audio: %w", err), secrets)
			coordinator.fail(failure)
			return failure
		}
		pcm := encodeRoomPCM16(roomSamples)
		if participantEvidence != nil {
			if evidenceErr := participantEvidence.observeAudio(pcm); evidenceErr != nil {
				failure := roomParticipantFailure(participantID, fmt.Errorf("record human input audio: %w", evidenceErr), secrets)
				coordinator.fail(failure)
				return failure
			}
		}
		for _, target := range coordinator.activeExcept(participantID) {
			if target == nil || target.mixer == nil {
				continue
			}
			targetPCM := pcm
			if target.mixer.Format() != runtime.mixer.Format() {
				targetSamples, convertErr := resampleRoomSamples(frame, audio.SampleRate, target.mixer.Format().SampleRate)
				if convertErr != nil {
					failure := roomParticipantFailure(participantID, fmt.Errorf("convert human input audio for %s: %w", target.plan.manifest.ID, convertErr), secrets)
					coordinator.fail(failure)
					return failure
				}
				targetPCM = encodeRoomPCM16(targetSamples)
			}
			if writeErr := target.mixer.WriteContext(runtime.ctx, participantID, targetPCM); writeErr != nil && coordinator.isActive(target.plan.manifest.ID) {
				failure := roomParticipantFailure(participantID, fmt.Errorf("fan out human PCM to %s: %w", target.plan.manifest.ID, writeErr), secrets)
				coordinator.fail(failure)
				return failure
			}
			if opts.onParticipantAudioFanned != nil {
				opts.onParticipantAudioFanned(participantID, target.plan.manifest.ID, append([]byte(nil), targetPCM...))
			}
		}
	}
}

func pumpRoomHumanOutput(ctx context.Context, coordinator *roomCoordinator, runtime *roomParticipantRuntime, startGate <-chan struct{}, secrets []string) {
	if runtime == nil || runtime.mixer == nil || runtime.output == nil {
		return
	}
	select {
	case <-startGate:
	case <-runtime.ctx.Done():
		return
	case <-ctx.Done():
		return
	}
	output := roomHumanOutputBuffer{}
	for {
		frame, err := runtime.mixer.ReadFrame(runtime.ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, room.ErrMixerClosed) || runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			coordinator.fail(roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("read human output mixer: %w", err), secrets))
			return
		}
		if err := output.writeFrame(runtime.ctx, runtime.output, runtime.mixer.Format(), frame); err != nil {
			if runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			coordinator.fail(roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("write human output device: %w", err), secrets))
			return
		}
	}
}

func encodeRoomPCM16(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

// resampleRoomSamples converts the fixed 16 kHz device contract to the room's
// configured mono clock (and back for playback). The shared wavio converter is
// used for production rates; the small generic fallback preserves the room
// package's arbitrary-rate deterministic test seam.
func resampleRoomSamples(samples []int16, inputRate, outputRate int) ([]int16, error) {
	if inputRate <= 0 || outputRate <= 0 {
		return nil, fmt.Errorf("audio sample rates must be positive: %d Hz to %d Hz", inputRate, outputRate)
	}
	if inputRate == outputRate {
		return append([]int16(nil), samples...), nil
	}
	if roomResampleRateSupported(inputRate) && roomResampleRateSupported(outputRate) {
		return wavio.Resample(samples, inputRate, outputRate)
	}
	if len(samples) == 0 {
		return []int16{}, nil
	}
	outputLengthFloat := math.Ceil(float64(len(samples)) * float64(outputRate) / float64(inputRate))
	maximumInt := int(^uint(0) >> 1)
	if outputLengthFloat > float64(maximumInt) {
		return nil, fmt.Errorf("audio resample output is too large: %g samples", outputLengthFloat)
	}
	outputLength := int(outputLengthFloat)
	converted := make([]int16, outputLength)
	for outputIndex := range converted {
		position := float64(outputIndex) * float64(inputRate) / float64(outputRate)
		sourceIndex := int(position)
		if sourceIndex >= len(samples)-1 {
			converted[outputIndex] = samples[len(samples)-1]
			continue
		}
		fraction := position - float64(sourceIndex)
		value := float64(samples[sourceIndex]) + (float64(samples[sourceIndex+1])-float64(samples[sourceIndex]))*fraction
		converted[outputIndex] = int16(math.Round(value))
	}
	return converted, nil
}

func roomResampleRateSupported(rate int) bool {
	return rate == wavio.Rate16kHz || rate == wavio.Rate24kHz || rate == wavio.Rate48kHz
}

type roomHumanOutputBuffer struct {
	pending []int16
}

func (b *roomHumanOutputBuffer) writeFrame(ctx context.Context, sink *audio.DeviceSink, format room.PCM16Format, pcm []byte) error {
	if sink == nil {
		return errors.New("human participant output device is nil")
	}
	if format == (room.PCM16Format{}) {
		format = room.DefaultPCM16Format()
	}
	if format.Channels != 1 {
		return fmt.Errorf("human output requires mono mixer audio, got %d channels", format.Channels)
	}
	if len(pcm)%2 != 0 {
		return errors.New("human output mixer produced an odd PCM16 frame")
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	converted, err := resampleRoomSamples(samples, format.SampleRate, audio.SampleRate)
	if err != nil {
		return fmt.Errorf("resample mixer audio from %d Hz to %d Hz: %w", format.SampleRate, audio.SampleRate, err)
	}
	b.pending = append(b.pending, converted...)
	for len(b.pending) >= audio.FrameSize {
		if err := sink.WriteFrame(ctx, b.pending[:audio.FrameSize]); err != nil {
			return err
		}
		b.pending = b.pending[audio.FrameSize:]
	}
	return nil
}
