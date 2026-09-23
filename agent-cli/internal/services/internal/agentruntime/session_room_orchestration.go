package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// RunRoom runs a manifest-defined room and discards the structured result.
func RunRoom(ctx context.Context, out io.Writer, opts RoomRunOptions) error {
	_, err := RunRoomWithResult(ctx, out, opts)
	return err
}

// RunRoomWithResult validates all participant configuration, constructs all
// session inferencers, establishes the local mesh, and then runs one persistent
// session per participant. No provider session is connected until every
// participant has passed validation and construction.
func RunRoomWithResult(ctx context.Context, out io.Writer, opts RoomRunOptions) (RoomResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}
	var err error
	validation := opts.Validation
	if opts.CredentialLookup != nil {
		validation.LookupCredential = opts.CredentialLookup
	}
	var replayMode bool
	opts, validation, replayMode, err = prepareRoomReplayOptions(opts, validation)
	if err != nil {
		result := roomFailureResult(err, nil)
		return result, err
	}
	opts, roomClock, err := validateRoomRunAdmission(opts, validation, replayMode)
	if err != nil {
		result := roomFailureResult(err, nil)
		return result, err
	}

	var evidence roomevidence.Recorder
	var recordingService = roomevidencewire.NewService()
	var latencyService = roomevidencewire.NewLatencyService()
	var evidenceSecrets []string
	startedAt := roomClock.Now().UTC()
	if strings.TrimSpace(opts.OutputDir) != "" {
		outputDir, outputErr := recordingService.PrepareOutput(opts.OutputDir)
		if outputErr != nil {
			result := roomFailureResult(outputErr, nil)
			return result, outputErr
		}
		opts.OutputDir = outputDir
		if !replayMode {
			evidenceSecrets = roomCredentialSecrets(opts.Manifest, validation)
		}
		format := roomFormatForOptions(opts)
		evidence, err = recordingService.Open(roomevidence.RecordingRequest{
			Destination: outputDir, Manifest: opts.Manifest, AudioFormat: roomevidence.AudioFormat{
				SampleRate: format.SampleRate, Channels: format.Channels, FrameDuration: format.FrameDuration,
			},
			Secrets: evidenceSecrets, StartedAt: startedAt, Clock: roomClock, Latency: latencyService,
		})
		if err != nil {
			result := roomFailureResult(err, evidenceSecrets)
			return result, err
		}
		if opts.onRoomEvidenceReady != nil {
			opts.onRoomEvidenceReady(evidence)
		}
	}
	finalizeEvidence := func(result RoomResult, runErr error) (RoomResult, error) {
		if evidence != nil {
			finalized, _ := evidence.Finalize(roomevidence.Finalization{Room: result, Err: runErr, EndedAt: roomClock.Now().UTC()})
			result = finalized.Room
		}
		return result, runErr
	}

	plans, secrets, err := buildRoomParticipantPlansWithContext(ctx, opts, validation, evidence)
	if err != nil {
		result := roomFailureResult(err, secrets)
		return finalizeEvidence(result, err)
	}
	replaySchedule, err := buildRoomReplaySchedule(ctx, replayMode, opts, plans)
	if err != nil {
		result := roomFailureResult(err, secrets)
		return finalizeEvidence(result, err)
	}

	// Keep caller cancellation out of participant contexts until the coordinator
	// has recorded the intentional room stop. Otherwise siblings can close their
	// transports first and be misclassified as provider disconnects.
	roomCtx, roomCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer roomCancel()
	mesh := room.NewParticipantMesh(roomCtx, opts.PairFactory)
	meshCloseClaimed := false
	defer func() {
		if !meshCloseClaimed {
			_ = mesh.Close()
		}
	}()
	closeMeshNow := func() error {
		if meshCloseClaimed {
			return nil
		}
		meshCloseClaimed = true
		cleanup := &roomCleanupWaiter{}
		cleanup.start()
		defer cleanup.stop()
		return closeRoomMeshBounded(mesh, cleanup)
	}
	for _, plan := range plans {
		if err := mesh.Join(roomCtx, plan.manifest.ID); err != nil {
			safeErr := roomParticipantFailure(plan.manifest.ID, err, secrets)
			safeErr = errors.Join(safeErr, closeMeshNow(), closeRoomParticipantPlanCapabilities(plans))
			result := roomFailureResult(safeErr, secrets)
			return finalizeEvidence(result, safeErr)
		}
		if evidence != nil {
			_ = evidence.RecordTimeline("participant_joined", plan.manifest.ID, nil)
		}
	}

	onParticipantTerminated := opts.OnParticipantTerminated
	if onParticipantTerminated != nil || evidence != nil {
		onParticipantTerminated = func(result RoomParticipantResult) {
			recordRoomParticipantBoundDiagnostic(opts, evidence, result)
			if evidence != nil {
				_ = evidence.RecordTimeline("participant_terminated", result.ParticipantID, participantTerminalFields(result))
			}
			if opts.OnParticipantTerminated != nil {
				opts.OnParticipantTerminated(result)
			}
		}
	}
	coordinator := newRoomCoordinator(roomCancel, opts.Manifest.Room.MaxTurns, opts.BoundShutdownGrace, onParticipantTerminated, opts.onRoomBoundShutdown)
	coordinator.setParticipantFailureObserver(func(participantID, reason string) {
		if evidence != nil {
			_ = evidence.RecordTimeline("participant_failed", participantID, map[string]string{"reason": reason})
		}
	})
	coordinator.blockEmptyStop()
	runtimes := make([]*roomParticipantRuntime, 0, len(plans))
	cleanupSetup := func() error {
		meshCloseClaimed = true
		cleanup := &roomCleanupWaiter{}
		cleanup.start()
		defer cleanup.stop()
		return errors.Join(cleanupRoomParticipantSetup(runtimes, mesh, cleanup), closeRoomParticipantPlanCapabilities(plans))
	}
	for _, plan := range plans {
		participantCtx, participantCancel := context.WithCancel(roomCtx)
		admissionCtx, admissionCancel := context.WithCancel(participantCtx)
		mixerConfig := roomReplayMixerConfig(opts, replaySchedule != nil)
		mixer, mixerErr := room.NewPCM16MixerWithConfig(participantCtx, mixerConfig)
		if mixerErr != nil {
			coordinator.fail(roomParticipantFailure(plan.manifest.ID, mixerErr, secrets))
			admissionCancel()
			participantCancel()
			roomErr := errors.Join(coordinator.roomError(), cleanupSetup())
			result := roomFailureResult(roomErr, secrets)
			return finalizeEvidence(result, roomErr)
		}
		runtime := newRoomParticipantRuntime(plan, participantCtx, participantCancel, admissionCtx, admissionCancel, mixer, replaySchedule, opts, evidence, coordinator)
		plan.participant = runtime
		if plan.tracker != nil {
			plan.tracker.lifecycle = runtime.lifecycle
		}
		runtimes = append(runtimes, runtime)
		coordinator.addParticipant(runtime)
		if plan.startupErr != nil {
			coordinator.failParticipant(plan.manifest.ID, plan.startupErr)
			continue
		}
		for _, other := range plans {
			if other.manifest.ID == plan.manifest.ID {
				continue
			}
			if addErr := mixer.AddInput(other.manifest.ID); addErr != nil {
				plan.startupErr = roomParticipantFailure(plan.manifest.ID, fmt.Errorf("configure participant mixer: %w", addErr), append(secretsForPlan(plan), secrets...))
				coordinator.failParticipant(plan.manifest.ID, plan.startupErr)
				break
			}
		}
		notifyRoomParticipantMixerReady(opts, plan.manifest.ID, mixer)
		if roomParticipantIsHuman(plan) && !replayMode {
			if deviceErr := openRoomHumanDevices(runtime, opts.DeviceService); deviceErr != nil {
				plan.startupErr = roomParticipantFailure(plan.manifest.ID, deviceErr, secretsForPlan(plan))
				coordinator.failParticipant(plan.manifest.ID, plan.startupErr)
				continue
			}
		} else if roomParticipantIsHuman(plan) {
			// A replayed human is represented by recorded artifacts only. Mark
			// the logical participant admitted without touching host audio.
			runtime.lifecycle.markDeviceReady()
		}
	}
	if replaySchedule == nil {
		coordinator.unblockEmptyStop()
	}

	// The room context is independent of caller cancellation; cancellation is
	// translated into the room's explicit stopped taxonomy before participant
	// loops observe it.
	go func() {
		select {
		case <-ctx.Done():
			coordinator.stop(RoomTerminationStopped, nil)
		case <-coordinator.done:
		}
	}()

	results := make(chan roomParticipantRunResult, len(plans))
	startGate := make(chan struct{})
	connectionOutcomes := make(chan roomConnectionOutcome, len(plans))
	for _, plan := range plans {
		if plan.tracker != nil {
			plan.tracker.setOutcomeSink(connectionOutcomes)
		}
	}
	var runWG sync.WaitGroup
	var mixerWG sync.WaitGroup
	var replayWG sync.WaitGroup
	runWG.Add(len(plans))
	mixerWG.Add(len(plans))
	for _, plan := range plans {
		go runRoomParticipant(roomCtx, coordinator, plan.participant, startGate, opts, evidence, results, &runWG, &mixerWG, secrets)
	}
	startRoomReplayScheduler(replaySchedule, roomCtx, startGate, runtimes, coordinator, opts, &replayWG)

	// A room duration bounds the initial connection phase as well as the live
	// conversation. The timer is stopped after every participant has returned.
	var timer *time.Timer
	if opts.Manifest.Room.MaxDuration > 0 {
		timer = time.NewTimer(opts.Manifest.Room.MaxDuration)
		defer timer.Stop()
	}

	cleanup := &roomCleanupWaiter{}
	defer cleanup.stop()
	startupErr := awaitRoomParticipantConnections(ctx, coordinator, plans, timer, secrets, connectionOutcomes, cleanup)
	if startupErr != nil {
		if coordinator.isStopping() {
			coordinator.recordError(startupErr)
		} else {
			coordinator.fail(startupErr)
		}
		cleanup.start()
	}
	if startupErr == nil && !coordinator.isStopping() {
		publishRoomParticipantsReady(coordinator, plans, opts, evidence)
	}
	close(startGate)
	replayWG.Wait()
	collectErr := collectRoomParticipantResults(ctx, coordinator, plans, mesh, secrets, timer, results, cleanup)
	workErr := waitRoomParticipantWork(&runWG, &mixerWG, plans, cleanup)
	cleanupErr := errors.Join(collectErr, workErr)
	if cleanupErr != nil {
		coordinator.recordError(cleanupErr)
	}
	meshCloseClaimed = true
	cleanupErr = errors.Join(cleanupErr, closeRoomMeshBounded(mesh, cleanup))
	reason, participantResults, active, roomErr := finalizeRoomParticipantResults(coordinator, plans, secrets, cleanupErr)
	if cleanupErr != nil && roomErr == nil {
		roomErr = cleanupErr
	}
	result := RoomResult{TerminationReason: reason, Reason: reason, Participants: participantResults, ActiveParticipants: append([]string(nil), active...)}
	if roomErr != nil {
		result.Error = sanitizeRoomError(roomErr, secrets)
	}
	result, roomErr = finalizeEvidence(result, roomErr)
	result, roomErr = notifyRoomTerminated(opts.OnRoomTerminated, result, roomErr, secrets)
	if _, writeErr := fmt.Fprintf(out, "room stopped: reason=%s participants=%d active=%d\n", result.Reason, len(result.Participants), len(result.ActiveParticipants)); writeErr != nil {
		roomErr = errors.Join(roomErr, fmt.Errorf("write room result: %w", writeErr))
	}
	return result, roomErr
}

func roomCredentialSecrets(manifest room.Manifest, options room.ValidationOptions) []string {
	lookup := options.LookupCredential
	if lookup == nil {
		lookup = os.LookupEnv
	}
	secrets := make([]string, 0, len(manifest.Participants))
	for _, participant := range manifest.Participants {
		if value, ok := lookup(participant.APIKeyEnv); ok && value != "" {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

// publishRoomParticipantsReady announces every started participant as ready:
// it enriches the evidence manifest with runtime-selected metadata, records
// the room-timeline transition, and notifies the stream/callback observers.
func publishRoomParticipantsReady(coordinator *roomCoordinator, plans []*roomParticipantPlan, opts RoomRunOptions, evidence roomevidence.Recorder) {
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		if coordinator != nil && !coordinator.isActive(plan.manifest.ID) {
			continue
		}
		ready := roomParticipantReady(plan)
		if evidence != nil {
			_ = evidence.SetParticipantReady(ready)
			_ = evidence.RecordTimeline("participant_ready", ready.ParticipantID, nil)
		}
		if opts.OnParticipantReady != nil {
			opts.OnParticipantReady(ready)
		}
	}
}

func roomReplayMixerConfig(opts RoomRunOptions, scheduled bool) room.PCM16MixerConfig {
	config := roomMixerConfigForOptions(opts)
	if scheduled {
		config.Manual = true
		config.CadenceFactory = nil
	}
	return config
}

// newRoomParticipantRuntime assembles one participant's routing and lifecycle
// state. Audio policy remains owned by the audio and device services.
func newRoomParticipantRuntime(
	plan *roomParticipantPlan,
	participantCtx context.Context,
	participantCancel context.CancelFunc,
	admissionCtx context.Context,
	admissionCancel context.CancelFunc,
	mixer *room.PCM16Mixer,
	replaySchedule roomreplay.Schedule,
	opts RoomRunOptions,
	evidence roomevidence.Recorder,
	coordinator *roomCoordinator,
) *roomParticipantRuntime {
	return &roomParticipantRuntime{
		plan:            plan,
		ctx:             participantCtx,
		cancel:          participantCancel,
		admissionCtx:    admissionCtx,
		admissionCancel: admissionCancel,
		loopReady:       make(chan *agentloop.AgentLoop, 1),
		participantDone: make(chan struct{}),
		mixerDone:       make(chan struct{}),
		observerDone:    make(chan struct{}),
		replayFrameAcks: roomReplayFrameAckChannel(replaySchedule, plan),
		mixer:           mixer,
		ingress:         newRoomParticipantIngress(plan, opts, evidence),
		lifecycle:       &roomParticipantLifecycle{stateChanged: coordinator.progress, admissionClosed: coordinator.admissionDone()},
	}
}

func roomReplayFrameAckChannel(schedule roomreplay.Schedule, plan *roomParticipantPlan) chan struct{} {
	if schedule == nil || roomParticipantIsHuman(plan) {
		return nil
	}
	return make(chan struct{}, 1)
}

func notifyRoomTerminated(observer RoomObserver, result RoomResult, roomErr error, secrets []string) (RoomResult, error) {
	if observer == nil {
		return result, roomErr
	}
	// The room observer is an external ownership boundary too. Keep a blocked
	// callback from turning an otherwise bounded room teardown into an unbounded
	// caller wait, while still giving it the complete result.
	observerCleanup := &roomCleanupWaiter{}
	observerCleanup.start()
	observerResult := &RoomResult{
		TerminationReason:  result.TerminationReason,
		Reason:             result.Reason,
		Participants:       result.Participants,
		ActiveParticipants: append([]string(nil), result.ActiveParticipants...),
		Error:              result.Error,
		RecordingStatus:    cloneRoomRecordingStatus(result.RecordingStatus),
		DegradedArtifacts:  maps.Clone(result.DegradedArtifacts),
	}
	observerErr := boundedRoomObserver(observerCleanup, "room observer", func() { observer(*observerResult) }, nil)
	observerCleanup.stop()
	if observerErr == nil {
		return result, roomErr
	}
	roomErr = errors.Join(roomErr, observerErr)
	result.TerminationReason = RoomTerminationFailed
	result.Reason = RoomTerminationFailed
	result.Error = sanitizeRoomError(roomErr, secrets)
	return result, roomErr
}

func cloneRoomRecordingStatus(status *transcript.RecordingStatus) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	copy := *status
	return &copy
}

func validateRoomRunAdmission(opts RoomRunOptions, validation room.ValidationOptions, replayMode bool) (RoomRunOptions, platformclock.Source, error) {
	if !replayMode {
		// A caller that already supplies its own session or transport seam
		// (SessionFactory, SessionInferencers, or WebSocketDialerFactory) owns
		// how — and whether — the room's first turn is triggered; the real CLI
		// launch path never sets any of these, so this can only relax the
		// opener requirement for a fully test-harnessed room, never for a live
		// provider-dialing run.
		harnessed := opts.SessionFactory != nil || len(opts.SessionInferencers) > 0 || opts.WebSocketDialerFactory != nil
		if harnessed {
			validation.AllowMissingOpener = true
		}
		if err := opts.Manifest.Validate(validation); err != nil {
			return opts, nil, err
		}
	}
	return normalizeRoomClockOptions(opts)
}

func normalizeRoomClockOptions(opts RoomRunOptions) (RoomRunOptions, platformclock.Source, error) {
	roomClock := platformclock.Ensure(opts.Clock)
	opts.Clock = roomClock
	if opts.LivenessClock == nil {
		if opts.AudioService == nil {
			return opts, roomClock, errors.New("audio service is required for room liveness timing")
		}
		opts.LivenessClock = sessiontracewire.LivenessClockFromSource(roomClock)
	}
	return opts, roomClock, nil
}

func roomParticipantReady(plan *roomParticipantPlan) RoomParticipantReady {
	if plan == nil {
		return RoomParticipantReady{}
	}
	participant := plan.manifest
	ready := RoomParticipantReady{
		ID:            participant.ID,
		ParticipantID: participant.ID,
		Kind:          room.NormalizeParticipantKind(participant.Kind),
		InputDevice:   participant.InputDevice,
		OutputDevice:  participant.OutputDevice,
		Provider:      participant.Provider,
		Model:         participant.Model,
	}
	if runtime := plan.participant; roomParticipantIsHuman(plan) && runtime != nil {
		if runtime.inputDeviceID != "" {
			ready.InputDevice = runtime.inputDeviceID
		}
		if runtime.outputDeviceID != "" {
			ready.OutputDevice = runtime.outputDeviceID
		}
	}
	return ready
}
