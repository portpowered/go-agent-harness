package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

func roomParticipantIsHuman(plan *roomParticipantPlan) bool {
	return plan != nil && room.NormalizeParticipantKind(plan.manifest.Kind) == room.ParticipantKindHuman
}

func buildRoomParticipantPlans(opts RoomRunOptions, validation room.ValidationOptions) ([]*roomParticipantPlan, []string, error) {
	return buildRoomParticipantPlansWithContext(context.Background(), opts, validation)
}

func buildRoomParticipantPlansWithContext(ctx context.Context, opts RoomRunOptions, validation room.ValidationOptions) (plans []*roomParticipantPlan, secrets []string, planErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if planErr != nil {
			planErr = errors.Join(planErr, closeRoomParticipantPlanCapabilities(plans))
		}
	}()
	if opts.ReplayPlan != nil {
		return buildRoomReplayParticipantPlans(ctx, *opts.ReplayPlan)
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
			Prompt:          participant.OpeningPrompt,
			Voice:           participant.Voice,
			WebSocketDialer: opts.WebSocketDialer,
			WaitForClose:    true,
		}
		var staticCapabilities RoomParticipantToolCapabilities
		if len(participant.Tools) > 0 {
			if toolFactory == nil {
				return plans, secrets, roomParticipantFailure(participant.ID, ErrRoomParticipantToolsUnavailable, []string{value})
			}
			capabilities, capabilityErr := toolFactory(participant)
			if capabilityErr != nil {
				return plans, secrets, roomParticipantFailure(participant.ID, fmt.Errorf("configure participant tools: %w", capabilityErr), []string{value})
			}
			if capabilityErr := validateRoomParticipantToolCapabilities(participant, capabilities); capabilityErr != nil {
				return plans, secrets, roomParticipantFailure(participant.ID, capabilityErr, []string{value})
			}
			staticCapabilities = capabilities
			sessionOptions.ToolExecutor = staticCapabilities.Executor
			sessionOptions.ToolDefinitions = cloneRoomToolDefinitions(staticCapabilities.Definitions)
		}
		if opts.WebSocketDialerFactory != nil {
			sessionOptions.WebSocketDialer = opts.WebSocketDialerFactory(participant)
		}
		plan := &roomParticipantPlan{manifest: participant, options: sessionOptions, secret: value}
		if participant.BrowserTools != nil {
			if opts.BrowserCapabilitiesFactory == nil {
				return plans, secrets, roomParticipantFailure(participant.ID, ErrRoomParticipantBrowserToolsUnavailable, []string{value})
			}
			browserCapabilities, capabilityErr := opts.BrowserCapabilitiesFactory(participant)
			if capabilityErr != nil {
				return plans, secrets, roomParticipantFailure(participant.ID, fmt.Errorf("configure browser tools: %w", capabilityErr), []string{value})
			}
			plan.capabilityCoordinator = NewSessionCapabilityCoordinator(browserCapabilities.Close)
			plans = append(plans, plan)
			if capabilityErr := validateRoomParticipantBrowserCapabilities(participant, browserCapabilities); capabilityErr != nil {
				return plans, secrets, roomParticipantFailure(participant.ID, capabilityErr, []string{value})
			}
			composed, capabilityErr := composeRoomParticipantBrowserCapabilities(participant, staticCapabilities, browserCapabilities)
			if capabilityErr != nil {
				return plans, secrets, roomParticipantFailure(participant.ID, capabilityErr, []string{value})
			}
			if composed.Initialize != nil {
				if initializeErr := composed.Initialize(ctx); initializeErr != nil {
					return plans, secrets, roomParticipantFailure(participant.ID, fmt.Errorf("initialize browser tools: %w", initializeErr), []string{value})
				}
			}
			if composed.RefreshToolDefinitions != nil {
				refreshed, refreshErr := composed.RefreshToolDefinitions(ctx)
				if refreshErr == nil {
					composed.Definitions = cloneRoomToolDefinitions(refreshed)
				} else if ctx.Err() != nil {
					return plans, secrets, roomParticipantFailure(participant.ID, fmt.Errorf("refresh browser tools: %w", refreshErr), []string{value})
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
				return plans, secrets, roomParticipantFailure(participant.ID, errors.New("injected session inferencer is nil"), []string{value})
			}
			plan.inferencer = inferencer
		} else {
			inferencer, factoryErr := factory(participant, sessionOptions)
			if factoryErr != nil {
				return plans, secrets, roomParticipantFailure(participant.ID, fmt.Errorf("construct live session: %w", factoryErr), []string{value})
			}
			if nilInterface(inferencer) {
				return plans, secrets, roomParticipantFailure(participant.ID, errors.New("session factory returned a nil inferencer"), []string{value})
			}
			plan.inferencer = inferencer
		}
		plan.tracker = newRoomConnectTrackingInferencer(plan.inferencer)
		if participant.BrowserTools == nil {
			plans = append(plans, plan)
		}
	}
	return plans, secrets, nil
}

// buildRoomReplayParticipantPlans composes each provider participant through
// the existing session replay planner. It deliberately does not consult the
// live room manifest, credential lookup, capability factories, or injected
// live session factories: the validated bundle is the complete source of
// replay runtime configuration.
func buildRoomReplayParticipantPlans(ctx context.Context, replay RoomReplayPlan) ([]*roomParticipantPlan, []string, error) {
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
			Prompt:         recorded.OpeningPrompt,
			PromptProvided: recorded.OpeningPrompt != "",
			Voice:          recorded.Voice,
			// Replay planning reads provider configuration from the captured
			// session.update. Keep ConfigDir and APIKey empty so no live config
			// or credential path can be consulted accidentally.
			ConfigDir:    "",
			WaitForClose: false,
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
		if plan != nil && plan.tracker != nil {
			byTracker[plan.tracker] = plan
		}
	}
	remaining := len(byTracker)
	seen := make(map[*roomConnectTrackingInferencer]struct{}, remaining)
	var firstErr error
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
			if outcome.err != nil && firstErr == nil {
				firstErr = roomParticipantFailure(plan.manifest.ID, fmt.Errorf("connect live session: %w", outcome.err), append(secretsForPlan(plan), secrets...))
			}
		case <-ctxDone:
			// A cancellation must still drain all already-admitted connection
			// attempts. Well-behaved inferencers observe the cancelled room
			// context and publish their explicit context-cancelled outcome.
			if firstErr != nil {
				coordinator.fail(firstErr)
			} else {
				coordinator.stop(RoomTerminationStopped, nil)
			}
			ctxDone = nil
		case <-timerDone:
			if firstErr != nil {
				coordinator.fail(firstErr)
			} else {
				coordinator.stop(RoomTerminationMaxDurationReached, nil)
			}
			timerDone = nil
		case <-admissionDone:
			if firstErr != nil {
				coordinator.fail(firstErr)
			} else {
				outstanding := make([]string, 0, remaining)
				for tracker, plan := range byTracker {
					if _, alreadySeen := seen[tracker]; alreadySeen || plan == nil {
						continue
					}
					outstanding = append(outstanding, roomLifecycleWorkLabel(plan.manifest.ID, "connect"))
				}
				coordinator.fail(newRoomLifecycleWorkError(outstanding...))
			}
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
	if firstErr != nil {
		return firstErr
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
			if roomParticipantIsHuman(plan) {
				if plan.participant.lifecycle.deviceHasReady() {
					continue
				}
				if plan.participant.lifecycle.runHasFinished() || coordinator.isStopping() {
					return roomParticipantFailure(plan.manifest.ID, errors.New("human participant devices were not ready"), append(secretsForPlan(plan), secrets...))
				}
				allOpened = false
				continue
			}
			_, opened, closed, _, _, _, _ := plan.participant.lifecycle.snapshot()
			if opened {
				continue
			}
			if closed || plan.participant.lifecycle.transportHasEnded() || plan.participant.lifecycle.runHasFinished() {
				if _, terminalErr, terminalObserved := plan.participant.lifecycle.terminal(); terminalObserved && terminalErr != nil {
					return roomParticipantFailure(plan.manifest.ID, terminalErr, append(secretsForPlan(plan), secrets...))
				}
				return roomParticipantFailure(plan.manifest.ID, errors.New("session ended before SESSION.OPEN"), append(secretsForPlan(plan), secrets...))
			}
			allOpened = false
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
				if roomParticipantIsHuman(plan) {
					if !plan.participant.lifecycle.deviceHasReady() {
						outstanding = append(outstanding, roomLifecycleWorkLabel(plan.manifest.ID, "devices"))
					}
					continue
				}
				_, opened, _, _, _, _, _ := plan.participant.lifecycle.snapshot()
				if !opened {
					outstanding = append(outstanding, roomLifecycleWorkLabel(plan.manifest.ID, "session.open"))
				}
			}
			return newRoomLifecycleWorkError(outstanding...)
		case <-coordinator.progress:
		}
	}
}
