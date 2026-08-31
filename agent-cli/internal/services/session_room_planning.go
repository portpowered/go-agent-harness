package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

func roomParticipantIsHuman(plan *roomParticipantPlan) bool {
	return plan != nil && room.NormalizeParticipantKind(plan.manifest.Kind) == room.ParticipantKindHuman
}

func buildRoomParticipantPlans(opts RoomRunOptions, validation room.ValidationOptions, evidences ...*roomEvidence) ([]*roomParticipantPlan, []string, error) {
	return buildRoomParticipantPlansWithContext(context.Background(), opts, validation, evidences...)
}

// buildRoomParticipantPlansWithContext accepts the room's evidence sink as an
// optional trailing argument (mirroring newRoomEvidence's own sources
// ...platformclock.Source pattern) so every existing two-argument call site
// -- almost all of them deterministic tests with no evidence bundle -- keeps
// compiling unchanged. When evidence is supplied and recording is not a
// replay, it is used to wire each live provider participant's websocket
// dialer for capture recording; see the loop below.
func buildRoomParticipantPlansWithContext(ctx context.Context, opts RoomRunOptions, validation room.ValidationOptions, evidences ...*roomEvidence) (plans []*roomParticipantPlan, secrets []string, planErr error) {
	var evidence *roomEvidence
	if len(evidences) > 0 {
		evidence = evidences[0]
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if planErr != nil {
			planErr = errors.Join(planErr, closeRoomParticipantPlanCapabilities(plans))
		}
	}()
	if opts.ReplayPlan != nil {
		return buildRoomReplayParticipantPlans(ctx, *opts.ReplayPlan, opts)
	}
	lookup := validation.LookupCredential
	if lookup == nil {
		lookup = os.LookupEnv
	}
	known := make(map[string]struct{}, len(opts.Manifest.Participants))
	secrets = make([]string, 0, len(opts.Manifest.Participants))
	for _, participant := range opts.Manifest.Participants {
		known[participant.ID] = struct{}{}
		if value, ok := lookup(participant.APIKeyEnv); ok && value != "" {
			secrets = append(secrets, value)
		}
	}
	for id := range opts.SessionInferencers {
		if _, ok := known[id]; !ok {
			return plans, secrets, fmt.Errorf("room session inferencer provided for unknown participant %q", id)
		}
	}
	toolFactory := opts.ToolCapabilitiesFactory
	if toolFactory == nil && roomManifestHasTools(opts.Manifest) {
		defaultFactory, factoryErr := newDefaultRoomParticipantToolCapabilitiesFactory(opts.ConfigDir)
		if factoryErr != nil {
			return plans, secrets, fmt.Errorf("%w: %v", ErrRoomParticipantToolsUnavailable, factoryErr)
		}
		toolFactory = defaultFactory
	}

	factory := opts.SessionFactory
	if factory == nil {
		factory = defaultRoomSessionFactory
	}
	plans = make([]*roomParticipantPlan, 0, len(opts.Manifest.Participants))
	for _, participant := range opts.Manifest.Participants {
		kind := room.NormalizeParticipantKind(participant.Kind)
		value, ok := lookup(participant.APIKeyEnv)
		if !ok {
			value = ""
		}
		if kind == room.ParticipantKindHuman {
			// Human participants own local capture/playback rather than a
			// provider session. Keep the manifest and its device selectors in
			// the plan, but do not construct a provider inferencer or resolve a
			// credential for this participant.
			plans = append(plans, &roomParticipantPlan{manifest: participant})
			continue
		}
		sessionOptions := SessionRunOptions{
			Provider:        participant.Provider,
			Model:           participant.Model,
			ModelProvided:   true,
			APIKey:          value,
			BaseURL:         opts.BaseURL,
			ConfigDir:       opts.ConfigDir,
			Clock:           opts.Clock,
			LivenessClock:   opts.LivenessClock,
			Prompt:          participant.OpeningPrompt,
			Voice:           participant.Voice,
			WebSocketDialer: opts.WebSocketDialer,
			WaitForClose:    true,
		}
		plan := &roomParticipantPlan{manifest: participant, options: sessionOptions, secret: value}
		plans = append(plans, plan)
		markStartupFailure := func(err error) {
			if err == nil {
				return
			}
			plan.startupErr = roomParticipantFailure(participant.ID, err, []string{value})
		}
		var staticCapabilities RoomParticipantToolCapabilities
		if len(participant.Tools) > 0 {
			if toolFactory == nil {
				markStartupFailure(ErrRoomParticipantToolsUnavailable)
				continue
			}
			capabilities, capabilityErr := toolFactory(participant)
			if capabilityErr != nil {
				markStartupFailure(fmt.Errorf("configure participant tools: %w", capabilityErr))
				continue
			}
			if capabilityErr := validateRoomParticipantToolCapabilities(participant, capabilities); capabilityErr != nil {
				if errors.Is(capabilityErr, ErrRoomParticipantToolMismatch) {
					// A mismatched advertised tool surface violates the room's
					// manifest/session contract. It is not a runtime failure that
					// can be isolated after admission.
					return plans, secrets, fmt.Errorf("room participant %q capability contract: %w", participant.ID, capabilityErr)
				}
				markStartupFailure(capabilityErr)
				continue
			}
			staticCapabilities = capabilities
			sessionOptions.ToolExecutor = staticCapabilities.Executor
			sessionOptions.ToolDefinitions = cloneRoomToolDefinitions(staticCapabilities.Definitions)
		}
		if opts.WebSocketDialerFactory != nil {
			sessionOptions.WebSocketDialer = opts.WebSocketDialerFactory(participant)
		}
		// Recording only applies on the genuine live-construction path
		// (evidence enabled, not a replay run): NewLiveSessionInferencer is
		// the only constructor that consults RecordSessionCapturePath. A
		// custom SessionFactory or an injected SessionInferencer (both
		// deterministic-test seams) ignore it, exactly like solo session
		// recording never applies to an injected inferencer either.
		if evidence != nil {
			if participantEvidence := evidence.participant(participant.ID); participantEvidence != nil && participantEvidence.artifacts.Capture != "" {
				sessionOptions.RecordSessionCapturePath = filepath.Join(evidence.destination, participantEvidence.artifacts.Capture)
			}
		}
		if participant.BrowserTools != nil {
			if opts.BrowserCapabilitiesFactory == nil {
				markStartupFailure(ErrRoomParticipantBrowserToolsUnavailable)
				continue
			}
			browserCapabilities, capabilityErr := opts.BrowserCapabilitiesFactory(participant)
			if capabilityErr != nil {
				markStartupFailure(fmt.Errorf("configure browser tools: %w", capabilityErr))
				continue
			}
			plan.capabilityCoordinator = NewSessionCapabilityCoordinator(browserCapabilities.Close)
			if capabilityErr := validateRoomParticipantBrowserCapabilities(participant, browserCapabilities); capabilityErr != nil {
				if errors.Is(capabilityErr, ErrRoomParticipantBrowserToolMismatch) {
					// Invalid browser definitions are a composition contract
					// failure. Do not admit a room whose advertised capability
					// surface cannot be routed safely.
					return plans, secrets, fmt.Errorf("room participant %q browser capability contract: %w", participant.ID, capabilityErr)
				}
				markStartupFailure(capabilityErr)
				continue
			}
			composed, capabilityErr := composeRoomParticipantBrowserCapabilities(participant, staticCapabilities, browserCapabilities)
			if capabilityErr != nil {
				return plans, secrets, fmt.Errorf("room participant %q browser composition contract: %w", participant.ID, capabilityErr)
			}
			if composed.Initialize != nil {
				if initializeErr := composed.Initialize(ctx); initializeErr != nil {
					markStartupFailure(fmt.Errorf("initialize browser tools: %w", initializeErr))
					continue
				}
			}
			if composed.RefreshToolDefinitions != nil {
				refreshed, refreshErr := composed.RefreshToolDefinitions(ctx)
				if refreshErr == nil {
					composed.Definitions = cloneRoomToolDefinitions(refreshed)
				} else if ctx.Err() != nil {
					markStartupFailure(fmt.Errorf("refresh browser tools: %w", refreshErr))
					continue
				}
			}
			sessionOptions.ToolExecutor = composed.Executor
			sessionOptions.ToolDefinitions = cloneRoomToolDefinitions(composed.Definitions)
			sessionOptions.ToolDefinitionBase = cloneRoomToolDefinitions(composed.ToolDefinitionBase)
			sessionOptions.RefreshToolDefinitions = composed.RefreshToolDefinitions
			sessionOptions.BrowserWatch = composed.BrowserWatch
			sessionOptions.BrowserToolsEnabled = true
			sessionOptions.CapabilityClose = plan.capabilityCoordinator.Close
		}
		plan.options = sessionOptions
		if inferencer, exists := opts.SessionInferencers[participant.ID]; exists {
			if nilInterface(inferencer) {
				markStartupFailure(errors.New("injected session inferencer is nil"))
				continue
			}
			plan.inferencer = inferencer
		} else {
			inferencer, factoryErr := factory(participant, sessionOptions)
			if factoryErr != nil {
				markStartupFailure(fmt.Errorf("construct live session: %w", factoryErr))
				continue
			}
			if nilInterface(inferencer) {
				markStartupFailure(errors.New("session factory returned a nil inferencer"))
				continue
			}
			plan.inferencer = inferencer
		}
		plan.tracker = newRoomConnectTrackingInferencer(plan.inferencer)
	}
	return plans, secrets, nil
}

// buildRoomReplayParticipantPlans composes each provider participant through
// the existing session replay planner. It deliberately does not consult the
// live room manifest, credential lookup, capability factories, or injected
// live session factories: the validated bundle is the complete source of
// replay runtime configuration.
func buildRoomReplayParticipantPlans(ctx context.Context, replay RoomReplayPlan, opts RoomRunOptions) ([]*roomParticipantPlan, []string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manifest := replay.Manifest()
	plans := make([]*roomParticipantPlan, 0, len(replay.Participants))
	for index, recorded := range replay.Participants {
		if err := ctx.Err(); err != nil {
			return plans, nil, err
		}
		if index >= len(manifest.Participants) {
			return plans, nil, roomParticipantFailure(recorded.ID, errors.New("replay participant projection is incomplete"), nil)
		}
		participant := manifest.Participants[index]
		plan := &roomParticipantPlan{
			manifest: participant,
			replay:   true,
		}
		if room.NormalizeParticipantKind(recorded.Kind) == room.ParticipantKindHuman {
			plans = append(plans, plan)
			continue
		}
		if recorded.CapturePath == "" {
			return plans, nil, roomParticipantFailure(recorded.ID, errors.New("replay provider capture path is empty"), nil)
		}
		sessionOptions := SessionRunOptions{
			Provider:       recorded.Provider,
			Model:          recorded.Model,
			ModelProvided:  true,
			ReplayPath:     recorded.CapturePath,
			roomReplay:     true,
			Prompt:         recorded.OpeningPrompt,
			PromptProvided: recorded.OpeningPrompt != "",
			Voice:          recorded.Voice,
			// Replay planning reads provider configuration from the captured
			// session.update. Keep ConfigDir and APIKey empty so no live config
			// or credential path can be consulted accidentally.
			ConfigDir:     "",
			Clock:         opts.Clock,
			LivenessClock: opts.LivenessClock,
			WaitForClose:  false,
		}
		runtimePlan, err := planSessionRuntime(sessionOptions)
		if err != nil {
			return plans, nil, roomParticipantFailure(recorded.ID, fmt.Errorf("plan replay session: %w", err), nil)
		}
		if nilInterface(runtimePlan.inferencer) {
			return plans, nil, roomParticipantFailure(recorded.ID, errors.New("replay session planner returned a nil inferencer"), nil)
		}
		plan.options = sessionOptions
		plan.inferencer = runtimePlan.inferencer
		plan.replayLoop = runtimePlan.loop
		// Room replay is bounded by the admitted capture's terminal boundary,
		// not by the ordinary live-session safety timeout.
		plan.replayLoop.MaxDuration = 0
		plan.tracker = newRoomConnectTrackingInferencer(plan.inferencer)
		plans = append(plans, plan)
	}
	return plans, nil, nil
}

func awaitRoomParticipantConnections(
	ctx context.Context,
	coordinator *roomCoordinator,
	plans []*roomParticipantPlan,
	timer *time.Timer,
	secrets []string,
	outcomes <-chan roomConnectionOutcome,
	cleanup *roomCleanupWaiter,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if cleanup == nil {
		cleanup = &roomCleanupWaiter{}
	}
	byTracker := make(map[*roomConnectTrackingInferencer]*roomParticipantPlan, len(plans))
	for _, plan := range plans {
		if plan != nil && plan.startupErr == nil && plan.tracker != nil {
			byTracker[plan.tracker] = plan
		}
	}
	remaining := len(byTracker)
	seen := make(map[*roomConnectTrackingInferencer]struct{}, remaining)
	ctxDone := ctx.Done()
	timerDone := timerChannel(timer)
	admissionTimer := time.NewTimer(roomAdmissionTimeout)
	defer admissionTimer.Stop()
	admissionDone := admissionTimer.C
	roomDone := coordinator.done
	for remaining > 0 {
		select {
		case outcome := <-outcomes:
			plan, ok := byTracker[outcome.tracker]
			if !ok {
				continue
			}
			if _, alreadySeen := seen[outcome.tracker]; alreadySeen {
				continue
			}
			seen[outcome.tracker] = struct{}{}
			remaining--
			if plan.participant != nil && plan.participant.lifecycle != nil {
				plan.participant.lifecycle.markConnected(outcome.err)
			}
			if outcome.err != nil {
				// A provider connection belongs to this participant. Retire only
				// its runtime and let any sibling that completed admission start.
				coordinator.failParticipant(plan.manifest.ID, roomParticipantFailure(plan.manifest.ID, fmt.Errorf("connect live session: %w", outcome.err), append(secretsForPlan(plan), secrets...)))
			}
		case <-ctxDone:
			// A cancellation must still drain all already-admitted connection
			// attempts. Well-behaved inferencers observe the cancelled room
			// context and publish their explicit context-cancelled outcome.
			coordinator.stop(RoomTerminationStopped, nil)
			ctxDone = nil
		case <-timerDone:
			coordinator.stop(RoomTerminationMaxDurationReached, nil)
			timerDone = nil
		case <-admissionDone:
			outstanding := make([]string, 0, remaining)
			for tracker, plan := range byTracker {
				if _, alreadySeen := seen[tracker]; alreadySeen || plan == nil {
					continue
				}
				outstanding = append(outstanding, roomLifecycleWorkLabel(plan.manifest.ID, "connect"))
			}
			// An admission timeout means the room-owned startup barrier itself
			// could not establish a safe participant set. Preserve that
			// room-level lifecycle diagnostic; a non-cooperative connection may
			// not have a bounded participant result to collect.
			coordinator.fail(newRoomLifecycleWorkError(outstanding...))
			admissionDone = nil
			cleanup.start()
		case <-roomDone:
			roomDone = nil
			cleanup.start()
		case <-cleanup.done():
			outstanding := make([]string, 0, remaining)
			for tracker, plan := range byTracker {
				if _, alreadySeen := seen[tracker]; alreadySeen || plan == nil {
					continue
				}
				outstanding = append(outstanding, roomLifecycleWorkLabel(plan.manifest.ID, "connect"))
			}
			for _, plan := range plans {
				if plan != nil {
					outstanding = append(outstanding, roomParticipantOutstandingWork(plan.participant)...)
				}
			}
			return newRoomLifecycleWorkError(outstanding...)
		}
	}
	if !admissionTimer.Stop() {
		select {
		case <-admissionTimer.C:
		default:
		}
	}
	if coordinator.isStopping() {
		return nil
	}
	readinessTimer := time.NewTimer(roomAdmissionTimeout)
	defer readinessTimer.Stop()
	readinessDone := readinessTimer.C
	for {
		allOpened := true
		for _, plan := range plans {
			if plan == nil || plan.participant == nil {
				continue
			}
			if !coordinator.isActive(plan.manifest.ID) {
				continue
			}
			ready, readinessErr := roomParticipantReadyForAdmission(coordinator, plan, secrets)
			if readinessErr != nil {
				return readinessErr
			}
				if !ready {
					allOpened = false
				}
		}
		if allOpened {
			return nil
		}
		select {
		case <-coordinator.done:
			if err := coordinator.roomError(); err != nil {
				return err
			}
			return nil
		case <-ctx.Done():
			coordinator.stop(RoomTerminationStopped, nil)
			return nil
		case <-timerChannel(timer):
			coordinator.stop(RoomTerminationMaxDurationReached, nil)
			return nil
		case <-readinessDone:
			outstanding := make([]string, 0, len(plans))
			for _, plan := range plans {
				if plan == nil || plan.participant == nil {
					continue
				}
				if !coordinator.isActive(plan.manifest.ID) {
					continue
				}
				if roomParticipantIsHuman(plan) {
					if !plan.participant.lifecycle.deviceHasReady() {
						coordinator.failParticipant(plan.manifest.ID, roomParticipantFailure(plan.manifest.ID, errors.New("human participant devices were not ready"), append(secretsForPlan(plan), secrets...)))
					}
					continue
				}
				_, opened, _, _, _, _, _ := plan.participant.lifecycle.snapshot()
				if !opened {
					outstanding = append(outstanding, plan.manifest.ID)
				}
			}
			for _, participantID := range outstanding {
				coordinator.failParticipant(participantID, roomParticipantFailure(participantID, errors.New("session did not become ready before admission deadline"), secrets))
			}
			if coordinator.isStopping() {
				return nil
			}
		case <-coordinator.progress:
		}
	}
}

func roomParticipantReadyForAdmission(coordinator *roomCoordinator, plan *roomParticipantPlan, secrets []string) (bool, error) {
	if plan == nil || plan.participant == nil || plan.participant.lifecycle == nil {
		return true, nil
	}
	lifecycle := plan.participant.lifecycle
	participantSecrets := append(secretsForPlan(plan), secrets...)
	if roomParticipantIsHuman(plan) {
		if lifecycle.deviceHasReady() {
			return true, nil
		}
		if lifecycle.runHasFinished() || coordinator.isStopping() {
			return false, roomParticipantFailure(plan.manifest.ID, errors.New("human participant devices were not ready"), participantSecrets)
		}
		return false, nil
	}
	_, opened, closed, _, _, _, _ := lifecycle.snapshot()
	if opened {
		return true, nil
	}
	transportEnded := lifecycle.transportHasEnded()
	runFinished := lifecycle.runHasFinished()
	if transportEnded && !runFinished {
		// A provider ERROR can make the engine stop and close its transport before
		// the session loop has finished draining the typed run error. Wait for that
		// bounded participant unwind before synthesizing a generic pre-open error.
		return false, nil
	}
	if closed || transportEnded || runFinished {
		if _, terminalErr, terminalObserved := lifecycle.terminal(); terminalObserved && terminalErr != nil {
			return false, roomParticipantFailure(plan.manifest.ID, terminalErr, participantSecrets)
		}
		return false, roomParticipantFailure(plan.manifest.ID, errors.New("session ended before SESSION.OPEN"), participantSecrets)
	}
	return false, nil
}
