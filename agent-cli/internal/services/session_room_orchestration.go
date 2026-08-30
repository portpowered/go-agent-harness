package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
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
	var streamTerminalOnce sync.Once
	publishStreamTermination := func(result RoomResult) {
		if opts.Stream == nil {
			return
		}
		streamTerminalOnce.Do(func() {
			opts.Stream.PublishRoomEvent(RoomStreamEventRunTerminated, RoomStreamRoomParticipantID, string(result.TerminationReason))
			_ = opts.Stream.Close()
		})
	}

	validation := opts.Validation
	if opts.CredentialLookup != nil {
		validation.LookupCredential = opts.CredentialLookup
	}
	if err := opts.Manifest.Validate(validation); err != nil {
		result := roomFailureResult(err, nil)
		publishStreamTermination(result)
		return result, err
	}
	if opts.Stream != nil {
		participantIDs := make([]string, 0, len(opts.Manifest.Participants))
		for _, participant := range opts.Manifest.Participants {
			participantIDs = append(participantIDs, participant.ID)
		}
		if err := opts.Stream.ValidateParticipants(participantIDs); err != nil {
			result := roomFailureResult(err, nil)
			publishStreamTermination(result)
			return result, err
		}
	}

	var evidence *roomEvidence
	var evidenceSecrets []string
	startedAt := time.Now().UTC()
	if strings.TrimSpace(opts.OutputDir) != "" {
		outputDir, outputErr := prepareRoomEvidenceOutput(opts.OutputDir)
		if outputErr != nil {
			result := roomFailureResult(outputErr, nil)
			publishStreamTermination(result)
			return result, outputErr
		}
		opts.OutputDir = outputDir
		evidenceSecrets = roomCredentialSecrets(opts.Manifest, validation)
		evidence, err = newRoomEvidence(outputDir, opts.Manifest, roomFormatForOptions(opts), evidenceSecrets, startedAt)
		if err != nil {
			result := roomFailureResult(err, evidenceSecrets)
			publishStreamTermination(result)
			return result, err
		}
	}
	finalizeEvidence := func(result RoomResult, runErr error) (RoomResult, error) {
		if evidence != nil {
			finalizeErr := evidence.finalize(result, runErr, time.Now().UTC())
			if finalizeErr != nil {
				runErr = errors.Join(runErr, finalizeErr)
				if result.Error == "" {
					result.Error = sanitizeRoomError(finalizeErr, evidenceSecrets)
				}
			}
		}
		publishStreamTermination(result)
		return result, runErr
	}

	plans, secrets, err := buildRoomParticipantPlansWithContext(ctx, opts, validation)
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
		if opts.Stream != nil {
			opts.Stream.PublishRoomEvent(RoomStreamEventParticipantJoined, plan.manifest.ID)
		}
	}

	onParticipantTerminated := opts.OnParticipantTerminated
	if opts.Stream != nil || onParticipantTerminated != nil {
		onParticipantTerminated = func(result RoomParticipantResult) {
			if opts.Stream != nil {
				opts.Stream.PublishRoomEvent(RoomStreamEventParticipantTerminated, result.ParticipantID, string(result.TerminationReason))
			}
			if opts.OnParticipantTerminated != nil {
				opts.OnParticipantTerminated(result)
			}
		}
	}
	coordinator := newRoomCoordinator(roomCancel, opts.Manifest.Room.MaxTurns, onParticipantTerminated)
	runtimes := make([]*roomParticipantRuntime, 0, len(plans))
	cleanupSetup := func() error {
		meshCloseClaimed = true
		cleanup := &roomCleanupWaiter{}
		cleanup.start()
		defer cleanup.stop()
		return errors.Join(cleanupRoomParticipantSetup(runtimes, mesh, cleanup), closeRoomParticipantPlanCapabilities(plans))
	}
	if evidence != nil {
		evidence.setErrorHandler(func(participantID string, evidenceErr error) {
			coordinator.fail(roomParticipantFailure(participantID, fmt.Errorf("record room evidence: %w", evidenceErr), secrets))
		})
	}
	for _, plan := range plans {
		participantCtx, participantCancel := context.WithCancel(roomCtx)
		mixerConfig := roomMixerConfigForOptions(opts)
		mixer, mixerErr := room.NewPCM16MixerWithConfig(participantCtx, mixerConfig)
		if mixerErr != nil {
			coordinator.fail(roomParticipantFailure(plan.manifest.ID, mixerErr, secrets))
			participantCancel()
			roomErr := errors.Join(coordinator.roomError(), cleanupSetup())
			result := roomFailureResult(roomErr, secrets)
			return finalizeEvidence(result, roomErr)
		}
		runtime := &roomParticipantRuntime{
			plan:            plan,
			ctx:             participantCtx,
			cancel:          participantCancel,
			loopReady:       make(chan *agentloop.AgentLoop, 1),
			participantDone: make(chan struct{}),
			mixerDone:       make(chan struct{}),
			observerDone:    make(chan struct{}),
			mixer:           mixer,
			lifecycle:       &roomParticipantLifecycle{stateChanged: coordinator.progress},
		}
		plan.participant = runtime
		plan.tracker.lifecycle = runtime.lifecycle
		runtimes = append(runtimes, runtime)
		for _, other := range plans {
			if other.manifest.ID == plan.manifest.ID {
				continue
			}
			if addErr := mixer.AddInput(other.manifest.ID); addErr != nil {
				coordinator.fail(roomParticipantFailure(plan.manifest.ID, addErr, secrets))
				roomErr := errors.Join(coordinator.roomError(), cleanupSetup())
				result := roomFailureResult(roomErr, secrets)
				return finalizeEvidence(result, roomErr)
			}
		}
		coordinator.addParticipant(runtime)
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
		plan.tracker.setOutcomeSink(connectionOutcomes)
	}
	var runWG sync.WaitGroup
	var mixerWG sync.WaitGroup
	runWG.Add(len(plans))
	mixerWG.Add(len(plans))
	for _, plan := range plans {
		go runRoomParticipant(roomCtx, coordinator, plan.participant, startGate, opts, evidence, results, &runWG, &mixerWG, secrets)
	}

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
	close(startGate)
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
	if opts.OnRoomTerminated != nil {
		// The room observer is an external ownership boundary too. Keep a
		// blocked callback from turning an otherwise bounded room teardown into
		// an unbounded caller wait, while still giving it the complete result.
		observerResult := result
		observerCleanup := &roomCleanupWaiter{}
		observerCleanup.start()
		observerErr := boundedRoomObserver(observerCleanup, "room observer", func() {
			opts.OnRoomTerminated(observerResult)
		}, nil)
		observerCleanup.stop()
		if observerErr != nil {
			roomErr = errors.Join(roomErr, observerErr)
			result.TerminationReason = RoomTerminationFailed
			result.Reason = RoomTerminationFailed
			result.Error = sanitizeRoomError(roomErr, secrets)
		}
	}
	if _, writeErr := fmt.Fprintf(out, "room stopped: reason=%s participants=%d\n", result.Reason, len(result.Participants)); writeErr != nil {
		roomErr = errors.Join(roomErr, fmt.Errorf("write room result: %w", writeErr))
	}
	return result, roomErr
}
