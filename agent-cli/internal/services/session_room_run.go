package services

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// recordRoomTimelineEvent turns one participant's inbound stream message
// into the room-level timeline entries that make a conversation's shape
// machine readable: response boundaries, barge-in cancel outcomes, and tool
// calls. It intentionally reads only already-observable inbound events (no
// go-agent-loop changes), matching what a real provider actually echoes back
// for a successful or failed RESPONSE.CANCEL.
func recordRoomTimelineEvent(evidence *roomEvidence, participantID string, msg messages.StreamMessage) {
	if evidence == nil || evidence.timeline == nil {
		return
	}
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		evidence.recordTimelineEvent("response_start", participantID, map[string]string{"response_id": msg.ResponseID})
	case messages.StreamTypeMessageEnd:
		fields := map[string]string{"response_id": msg.ResponseID}
		if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
			fields["terminal_reason"] = string(value.TerminalReason)
			fields["output_state"] = string(value.OutputState)
			evidence.recordTimelineEvent("response_end", participantID, fields)
			if value.TerminalReason == messages.TerminalReasonCancellation {
				// A provider only reports a response as cancelled when a
				// RESPONSE.CANCEL it received actually took effect: this is
				// the barge-in cancel's acknowledgement.
				evidence.recordTimelineEvent("barge_in_cancel_acked", participantID, map[string]string{"response_id": msg.ResponseID})
			}
			return
		}
		evidence.recordTimelineEvent("response_end", participantID, fields)
	case messages.StreamTypeError:
		value, ok := msg.Value.(*messages.ErrorValue)
		if !ok || value == nil {
			return
		}
		fields := map[string]string{"code": value.Code, "classification": value.Classification, "message": value.Message}
		if value.Classification == providers.ErrorClassResponseCancelNotActive {
			// The provider rejected a barge-in cancel because it had no
			// active response to cancel: an observable cancel failure.
			evidence.recordTimelineEvent("barge_in_cancel_failed", participantID, fields)
			return
		}
		evidence.recordTimelineEvent("provider_error", participantID, fields)
	case messages.StreamTypeToolCallStart:
		evidence.recordTimelineEvent("tool_call_start", participantID, map[string]string{"tool_call_id": msg.ToolCallId})
	case messages.StreamTypeToolCallEnd:
		evidence.recordTimelineEvent("tool_call_end", participantID, map[string]string{"tool_call_id": msg.ToolCallId})
	}
}

type roomParticipantRunResult struct {
	plan       *roomParticipantPlan
	runtime    *roomParticipantRuntime
	err        error
	connected  bool
	connectErr error
}

func defaultRoomSessionFactory(participant room.Participant, options SessionRunOptions) (messages.SessionInferencer, error) {
	if options.ReplayPath != "" {
		plan, err := planSessionRuntime(options)
		if err != nil {
			return nil, err
		}
		return plan.inferencer, nil
	}
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
	participantEvidence := (*roomParticipantEvidence)(nil)
	if evidence != nil {
		participantEvidence = evidence.participant(runtime.plan.manifest.ID)
	}
	participantStream := RoomParticipantEventSink{}
	if opts.Stream != nil {
		participantStream = opts.Stream.ParticipantSink(runtime.plan.manifest.ID)
	}
	var observer *sessionProgressObserver
	if !roomParticipantIsHuman(runtime.plan) {
		diagnosticSinks := roomParticipantDiagnosticSinks(runtime.plan, opts, participantEvidence, participantStream)
		observer = newSessionProgressObserver(combineRoomDiagnosticSinks(diagnosticSinks...), nil, runtime.plan.manifest.Provider, runtime.plan.manifest.Model)
		observer.livenessObserver = func(err error) {
			runtime.lifecycle.markLivenessFailure(err)
			classification, _, _, _ := sessionLivenessMetadata(err)
			if classification != "" {
				participantID := runtime.plan.manifest.ID
				evidence.recordTimelineEvent(RoomStreamEventParticipantLivenessFault, participantID, map[string]string{"reason": classification})
				if opts.Stream != nil {
					opts.Stream.PublishParticipantLivenessFault(participantID, classification)
				}
			}
		}
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
			evidence.recordTimelineEvent("turn_completed", runtime.plan.manifest.ID, map[string]string{"turn_index": strconv.Itoa(turns)})
		}
	}
	inputObserver := opts.OnAudioInput
	if observer != nil {
		inputObserver = func(participantID string, pcm []byte) error {
			observer.accountRoomAudioInput(len(pcm))
			if opts.OnAudioInput != nil {
				return opts.OnAudioInput(participantID, pcm)
			}
			return nil
		}
	}
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
			pumpRoomHumanOutput(roomCtx, coordinator, runtime, startGate, participantEvidence, secrets)
			return
		}
		pumpRoomMixer(roomCtx, coordinator, runtime, startGate, opts.onParticipantAudioInput, inputObserver, participantEvidence, secrets)
	}()

	if startupErr := runtime.plan.startupErr; startupErr != nil {
		// Setup failures are already retired from the coordinator's active set.
		// Still use the ordinary result path so participant cleanup, evidence
		// finalization, and the terminal observer remain exactly once.
		if closeErr := closeRoomParticipantCapability(runtime.plan); closeErr != nil {
			startupErr = errors.Join(startupErr, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("close browser tools: %w", closeErr), secretsForPlan(runtime.plan)))
		}
		runtime.lifecycle.markParticipantFailure(startupErr)
		runtime.lifecycle.markRunDone(startupErr)
		results <- roomParticipantRunResult{
			plan:       runtime.plan,
			runtime:    runtime,
			err:        startupErr,
			connected:  false,
			connectErr: nil,
		}
		return
	}

	if roomParticipantIsHuman(runtime.plan) {
		if runtime.plan.replay {
			select {
			case <-startGate:
			case <-runtime.ctx.Done():
			case <-roomCtx.Done():
			}
			runtime.lifecycle.markRunDone(nil)
			connected, _, _, _, _, _, connectErr := runtime.lifecycle.snapshot()
			results <- roomParticipantRunResult{plan: runtime.plan, runtime: runtime, connected: connected, connectErr: connectErr}
			return
		}
		runErr := runRoomHumanCapture(roomCtx, coordinator, runtime, startGate, participantEvidence, opts, secrets)
		runErr = coordinator.participantRunError(runtime.plan.manifest.ID, runErr)
		runtime.lifecycle.markRunDone(runErr)
		connected, _, _, _, _, _, connectErr := runtime.lifecycle.snapshot()
		results <- roomParticipantRunResult{plan: runtime.plan, runtime: runtime, err: runErr, connected: connected, connectErr: connectErr}
		return
	}
	var latencyRuntime *sessionRuntimeObservationRecorder
	if evidence != nil && evidence.latency != nil {
		latencyRuntime = newSessionRuntimeObservationRecorder(roomLatencyRuntimeObserver{
			recorder:      evidence.latency,
			participantID: runtime.plan.manifest.ID,
		}, opts.Clock)
		latencyRuntime.enableProviderBoundaryObservations()
		// Latency sampling is a live-path measurement only. A replayed room
		// drives its own scheduler off the recorded timeline, so attaching the
		// runtime observer there emits outbound audio after replay completed.
		if !runtime.plan.replay {
			observer.runtime = latencyRuntime
		}
	}
	loopOptions := sessionLoopOptions{
		Prompt:                 runtime.plan.options.Prompt,
		livenessClock:          runtime.plan.options.LivenessClock,
		WaitForClose:           true,
		Done:                   coordinator.done,
		DoneErr:                coordinator.roomError,
		AdmissionClosed:        coordinator.admissionDone(),
		BoundCancellation:      coordinator.boundCancellationDone(),
		ToolExecutor:           runtime.plan.options.ToolExecutor,
		ToolDefinitions:        cloneRoomToolDefinitions(runtime.plan.options.ToolDefinitions),
		ToolDefinitionBase:     cloneRoomToolDefinitions(runtime.plan.options.ToolDefinitionBase),
		RefreshToolDefinitions: runtime.plan.options.RefreshToolDefinitions,
		BrowserWatch:           runtime.plan.options.BrowserWatch,
	}
	if runtime.plan.replay {
		loopOptions = runtime.plan.replayLoop
		loopOptions.MaxDuration = 0
		loopOptions.Done = combineRoomDoneChannels(coordinator.done, loopOptions.Done)
		loopOptions.DoneErr = combineRoomDoneErrors(coordinator.roomError, loopOptions.DoneErr)
		if runtime.replayFrameAcks == nil && runtime.mixer != nil {
			// Text-only room replays still start the ordinary mixer pump. Stop
			// that producer before the session boundary's quiet drain so its
			// synthetic silence cannot become an unexpected outbound audio event
			// after the capture has completed.
			loopOptions.quiesceUpstream = runtime.mixer.Close
		}
	}
	loopOptions.observer = observer
	loopOptions.loopReady = runtime.loopReady
	runErr := runAgentLoopSession(runtime.ctx, io.Discard, runtime.plan.tracker, loopOptions)
	// The provider session this participant's inferencer produced is fully
	// closed at this point (runAgentLoopSession only returns after the loop
	// has finished, including session close). Flushing the capture here --
	// rather than earlier -- is what makes it reflect the complete exchange
	// instead of a possibly-truncated one.
	if flusher, ok := runtime.plan.inferencer.(SessionInferencerCaptureFlusher); ok {
		if flushErr := flusher.FlushCapture(); flushErr != nil && participantEvidence != nil {
			participantEvidence.recordError(participantEvidence.artifacts.Capture, flushErr)
		}
	}
	if closeErr := closeRoomParticipantCapability(runtime.plan); closeErr != nil {
		runErr = errors.Join(runErr, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("close browser tools: %w", closeErr), secretsForPlan(runtime.plan)))
	}
	runErr = coordinator.participantRunError(runtime.plan.manifest.ID, runErr)
	if runErr != nil && !roomCancellationOnly(runErr) {
		coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, runErr, secretsForPlan(runtime.plan)))
	}
	runtime.lifecycle.markRunDone(runErr)
	connected, _, _, _, _, _, connectErr := runtime.lifecycle.snapshot()
	if trackedErr, ready := runtime.plan.tracker.outcome(); ready {
		connectErr = trackedErr
		runtime.lifecycle.markConnected(connectErr)
		connected = connectErr == nil
	}
	results <- roomParticipantRunResult{plan: runtime.plan, runtime: runtime, err: runErr, connected: connected, connectErr: connectErr}
}

func combineRoomDoneChannels(primary, secondary <-chan struct{}) <-chan struct{} {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-primary:
		case <-secondary:
		}
		close(done)
	}()
	return done
}

func combineRoomDoneErrors(primary, secondary func() error) func() error {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return func() error {
		return errors.Join(primary(), secondary())
	}
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
	if evidence != nil && msg.Type == messages.StreamTypeVADSpeechStopped {
		evidence.observeSpeechStopped(plan.manifest.ID)
	}
	if opts.onParticipantStream != nil {
		opts.onParticipantStream(plan.manifest.ID, msg)
	}
	if opts.Stream != nil {
		participantStream.ObserveStream(msg)
	}
	recordParticipantDelta := func() {
		if participantEvidence == nil {
			return
		}
		// A recording sink is observational. Its failure is retained by the
		// participant evidence status, but never changes this participant's
		// runtime outcome or cancellation context.
		_ = participantEvidence.observeDelta(msg)
	}
	runtime.lifecycle.observe(msg)
	recordRoomTimelineEvent(evidence, plan.manifest.ID, msg)
	if msg.Type == messages.StreamTypeSessionOpen && opts.onParticipantSessionOpen != nil {
		opts.onParticipantSessionOpen(plan.manifest.ID)
	}
	if msg.Type == messages.StreamTypeAudioEnd && assistantAudioDelta(msg) && participantEvidence != nil {
		// A provider audio segment can end without ever emitting a silent
		// trailing chunk, so the energy-based tracker alone would never see
		// the transition back to silence. AUDIO.END is the reliable signal
		// that this participant's own speech segment is over.
		participantEvidence.closeSentSpeechSegment()
	}
	if msg.Type != messages.StreamTypeAudioDelta || !assistantAudioDelta(msg) {
		recordParticipantDelta()
		return
	}
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok || value == nil {
		recordParticipantDelta()
		coordinator.failParticipant(plan.manifest.ID, roomParticipantFailure(plan.manifest.ID, fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value), secretsForPlan(plan)))
		return
	}
	pcm := append([]byte(nil), value.Content...)
	targets := coordinator.activeExcept(plan.manifest.ID)
	targetIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if target != nil && target.plan != nil {
			targetIDs = append(targetIDs, target.plan.manifest.ID)
		}
	}
	if evidence != nil {
		evidence.observeSpeakerAudio(plan.manifest.ID, targetIDs, pcm)
		if len(pcm) > 0 {
			evidence.observeProviderAudio(plan.manifest.ID, msg.ResponseID)
		}
	}
	if participantEvidence != nil {
		// The sent-PCM stream, room mix, and speech timeline stay on the
		// critical path: they are offset-anchored, so deferring them past the
		// handoff would misplace this participant's audio in the room mix.
		// The WAV write, which nothing else is ordered against, moves below.
		_ = participantEvidence.observeSentStream(pcm)
	}
	if opts.OnAudioOutput != nil {
		if outputErr := opts.OnAudioOutput(plan.manifest.ID, append([]byte(nil), pcm...)); outputErr != nil {
			coordinator.failParticipant(plan.manifest.ID, roomParticipantFailure(plan.manifest.ID, outputErr, secretsForPlan(plan)))
			return
		}
	}
	// Room replay audio is released by the single room scheduler from the
	// recorded logical timeline. Provider output remains observable above, but
	// independently fanning it here would let goroutine timing choose the
	// cross-participant order and overlap. Only the fan-out is skipped: the
	// durable evidence writes below still run on the replay path, so a replayed
	// room produces the same artifacts as the live room it was recorded from.
	if !plan.replay {
		for _, target := range targets {
			if target == nil || target.mixer == nil {
				continue
			}
			if writeErr := routeRoomPeerPCM(runtime.ctx, plan.manifest.ID, target, pcm); writeErr != nil {
				if coordinator.isActive(target.plan.manifest.ID) {
					coordinator.failParticipant(target.plan.manifest.ID, roomParticipantFailure(target.plan.manifest.ID, fmt.Errorf("receive fan out PCM from %s: %w", plan.manifest.ID, writeErr), secretsForPlan(target.plan)))
				}
				continue
			}
			if evidence != nil {
				evidence.observePeerAudio(plan.manifest.ID, target.plan.manifest.ID, pcm)
			}
			if opts.onParticipantAudioFanned != nil {
				opts.onParticipantAudioFanned(plan.manifest.ID, target.plan.manifest.ID, append([]byte(nil), pcm...))
			}
		}
	}
	// Durable JSONL/WAV evidence is intentionally recorded after the bounded
	// provider-to-peer handoff. A slow filesystem must not make the next room
	// mixer frame wait before it can accept the first provider PCM delta.
	recordParticipantDelta()
	if participantEvidence != nil {
		// Durable WAV I/O only. A slow filesystem here must not delay the
		// provider-to-peer handoff above.
		_ = participantEvidence.observeAudio(pcm)
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
		participantResult := RoomParticipantResult{
			ID:                plan.manifest.ID,
			ParticipantID:     plan.manifest.ID,
			TerminationReason: participantReason,
			Reason:            participantReason,
			TurnsCompleted:    turns,
			Connected:         connected,
			Error:             sanitizeRoomError(errors.Join(connectErr, completionErr), secretsForPlan(plan)),
		}
		applyRoomParticipantTerminalMetadata(&participantResult, plan.participant.lifecycle, errors.Join(connectErr, completionErr))
		participantResults[plan.manifest.ID] = participantResult
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
			if !coordinator.isStopping() {
				failure := result.connectErr
				if failure == nil && result.err != nil && !roomCancellationOnly(result.err) {
					failure = result.err
				}
				if failure != nil {
					coordinator.failParticipant(id, roomParticipantFailure(id, failure, append(secretsForPlan(result.plan), secrets...)))
				}
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
		for _, plan := range plans {
			if plan != nil && plan.participant != nil && plan.participant.ingress != nil {
				plan.participant.ingress.finish()
			}
		}
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
		if runtime.admissionCancel != nil {
			runtime.admissionCancel()
		}
		if runtime.cancel != nil {
			runtime.cancel()
		}
		cleanupErr = errors.Join(cleanupErr, closeRoomParticipantDevices(runtime, cleanup))
		if runtime.mixer != nil {
			cleanupErr = errors.Join(cleanupErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(runtime.plan.manifest.ID, "mixer"), runtime.mixer.Close))
		}
		if runtime.ingress != nil {
			runtime.ingress.finish()
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

func pumpRoomMixer(ctx context.Context, coordinator *roomCoordinator, runtime *roomParticipantRuntime, startGate <-chan struct{}, inputHook func(string, []byte) error, observer RoomParticipantAudioObserver, participantEvidence *roomParticipantEvidence, secrets []string) {
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
		coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, errors.New("room session loop did not become ready"), secretsForPlan(runtime.plan)))
		return
	}
	sendAudioInput := loop.SendAudioInput
	if inputHook != nil {
		sendAudioInput = func(_ context.Context, pcm []byte) error {
			return inputHook(runtime.plan.manifest.ID, append([]byte(nil), pcm...))
		}
	}
	for {
		admissionCtx := runtime.admissionCtx
		if admissionCtx == nil {
			admissionCtx = runtime.ctx
		}
		mixed, err := runtime.mixer.ReadFrameWithSources(admissionCtx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, room.ErrMixerClosed) || runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("read inbound mixer: %w", err), secrets))
			return
		}
		frame := mixed.PCM
		if err := sendAudioInput(admissionCtx, frame); err != nil {
			if runtime.ingress != nil {
				runtime.ingress.resolveFrame(mixed.Sources, len(frame), roomAudioIngressReasonProviderInputRejected)
			}
			if runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			// Make a dropped delivery of real (non-silent) incoming audio an
			// explicit, diagnosable event instead of leaving it
			// indistinguishable from ordinary silence.
			if participantEvidence != nil && pcm16HasSignal(frame) {
				participantEvidence.recordAudioDropped(err.Error(), len(frame))
			}
			coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("send mixed PCM: %w", err), secretsForPlan(runtime.plan)))
			return
		}
		if runtime.ingress != nil {
			// The mixer admission is provisional until SendAudioInput accepts
			// this exact mixed frame. resolveFrame retains each peer's source
			// identity and original delivered/backpressured disposition.
			runtime.ingress.resolveFrame(mixed.Sources, len(frame), "")
		}
		if participantEvidence != nil {
			// received.pcm is the provider-bound artifact. Record it only after
			// SendAudioInput succeeds so a downstream rejection cannot create a
			// false received frame.
			_ = participantEvidence.observeReceivedAudio(frame)
		}
		if observer != nil {
			if err := observer(runtime.plan.manifest.ID, append([]byte(nil), frame...)); err != nil {
				coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("observe mixed PCM: %w", err), secretsForPlan(runtime.plan)))
				return
			}
		}
		if runtime.replayFrameAcks != nil {
			select {
			case runtime.replayFrameAcks <- struct{}{}:
			case <-runtime.ctx.Done():
				return
			case <-ctx.Done():
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
			coordinator.failParticipant(participantID, failure)
			return failure
		}
		roomSamples, err := resampleRoomSamples(frame, audio.SampleRate, runtime.mixer.Format().SampleRate)
		if err != nil {
			failure := roomParticipantFailure(participantID, fmt.Errorf("convert human input audio: %w", err), secrets)
			coordinator.failParticipant(participantID, failure)
			return failure
		}
		pcm := encodeRoomPCM16(roomSamples)
		if participantEvidence != nil {
			// Evidence is best-effort and independent of human capture/fan-out.
			_ = participantEvidence.observeSentAudio(pcm)
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
					coordinator.failParticipant(participantID, failure)
					return failure
				}
				targetPCM = encodeRoomPCM16(targetSamples)
			}
			if writeErr := routeRoomPeerPCM(runtime.ctx, participantID, target, targetPCM); writeErr != nil {
				if coordinator.isActive(target.plan.manifest.ID) {
					failure := roomParticipantFailure(target.plan.manifest.ID, fmt.Errorf("receive fan out human PCM from %s: %w", participantID, writeErr), secrets)
					coordinator.failParticipant(target.plan.manifest.ID, failure)
					return failure
				}
				continue
			}
			if opts.onParticipantAudioFanned != nil {
				opts.onParticipantAudioFanned(participantID, target.plan.manifest.ID, append([]byte(nil), targetPCM...))
			}
		}
	}
}

func pumpRoomHumanOutput(ctx context.Context, coordinator *roomCoordinator, runtime *roomParticipantRuntime, startGate <-chan struct{}, participantEvidence *roomParticipantEvidence, secrets []string) {
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
		mixed, err := runtime.mixer.ReadFrameWithSources(runtime.ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, room.ErrMixerClosed) || runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("read human output mixer: %w", err), secrets))
			return
		}
		frame := mixed.PCM
		if err := output.writeFrame(runtime.ctx, runtime.output, runtime.mixer.Format(), frame); err != nil {
			if runtime.ingress != nil {
				runtime.ingress.resolveFrame(mixed.Sources, len(frame), roomAudioIngressReasonParticipantOutputRejected)
			}
			if runtime.ctx.Err() != nil || coordinator.isStopping() {
				return
			}
			coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("write human output device: %w", err), secrets))
			return
		}
		if runtime.ingress != nil {
			runtime.ingress.resolveFrame(mixed.Sources, len(frame), "")
		}
		if participantEvidence != nil {
			_ = participantEvidence.observeReceivedAudio(frame)
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
