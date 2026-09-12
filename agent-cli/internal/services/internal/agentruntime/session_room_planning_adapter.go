package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimePlanning "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	runtimePlanningWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning/wire"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func roomParticipantIsHumanAdapter(plan *roomParticipantPlan) bool {
	return plan != nil && room.NormalizeParticipantKind(plan.manifest.Kind) == room.ParticipantKindHuman
}

func buildRoomParticipantPlansAdapter(ctx context.Context, opts RoomRunOptions, validation room.ValidationOptions, evidences ...*roomEvidence) (plans []*roomParticipantPlan, secrets []string, planErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	policy := opts.FilesystemPolicy
	if policy == nil {
		var err error
		policy, err = tools.ResolveFilesystemPolicy(opts.WorkDir, opts.AllowPaths...)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve filesystem scope: %w", err)
		}
	}
	opts.FilesystemPolicy, opts.WorkDir, opts.AllowPaths = policy, policy.PrimaryRoot(), policy.AdditionalRoots()
	lookup := validation.LookupCredential
	if lookup == nil {
		lookup = opts.CredentialLookup
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	credentialByID, idsByEnv := map[string]string{}, map[string][]string{}
	credentialValues, credentialOK := map[string]string{}, map[string]bool{}
	for _, participant := range opts.Manifest.Participants {
		if participant.APIKeyEnv != "" {
			idsByEnv[participant.APIKeyEnv] = append(idsByEnv[participant.APIKeyEnv], participant.ID)
		}
	}
	recordLookup := func(name string) (string, bool) {
		if _, lookedUp := credentialValues[name]; lookedUp {
			return credentialValues[name], credentialOK[name]
		}
		value, ok := lookup(name)
		credentialValues[name], credentialOK[name] = value, ok
		if ok && value != "" {
			for _, id := range idsByEnv[name] {
				credentialByID[id] = value
			}
		}
		return value, ok
	}
	if opts.ReplayPlan == nil {
		for _, participant := range opts.Manifest.Participants {
			if participant.APIKeyEnv != "" {
				_, _ = recordLookup(participant.APIKeyEnv)
			}
		}
	}
	participantError := func(id string, err error) error { return roomParticipantFailure(id, err, []string{credentialByID[id]}) }
	usesProductionFactory := opts.SessionFactory == nil && opts.WebSocketDialerFactory == nil
	sessionFactory := opts.SessionFactory
	if sessionFactory == nil {
		sessionFactory = defaultRoomSessionFactory
	}
	toolFactory := opts.ToolCapabilitiesFactory
	if toolFactory == nil && roomManifestHasTools(opts.Manifest) {
		var err error
		toolFactory, err = newDefaultRoomParticipantToolCapabilitiesFactoryWithPolicy(opts.ConfigDir, policy)
		if err != nil {
			return nil, roomSecretsInManifest(opts.Manifest, credentialByID), fmt.Errorf("%w: %v", ErrRoomParticipantToolsUnavailable, err)
		}
	}
	var evidence *roomEvidence
	if len(evidences) > 0 {
		evidence = evidences[0]
	}
	publicOptions := runtimePlanning.Options{
		Manifest: opts.Manifest, LookupCredential: recordLookup, Filesystem: &runtimePlanning.FilesystemScope{PrimaryRoot: policy.PrimaryRoot(), AdditionalRoots: policy.AdditionalRoots()},
		WorkDir: opts.WorkDir, AllowPaths: opts.AllowPaths, ConfigDir: opts.ConfigDir, BaseURL: opts.BaseURL, WebSocketDialer: opts.WebSocketDialer, WebSocketDialerFactory: opts.WebSocketDialerFactory,
		ParticipantError: participantError, SessionInferencers: opts.SessionInferencers,
		SessionFactory: func(request runtimePlanning.LiveSessionRequest) (messages.SessionInferencer, error) {
			return sessionFactory(request.Participant, roomSessionOptionsFromRuntime(opts, request.Options, request.Credential))
		},
		ToolFactory: func(participant room.Participant) (runtimePlanning.ToolCapabilities, error) {
			if toolFactory == nil {
				return runtimePlanning.ToolCapabilities{}, runtimePlanning.ErrParticipantTools
			}
			capabilities, err := toolFactory(participant)
			return runtimePlanning.ToolCapabilities{Executor: capabilities.Executor, Definitions: capabilities.Definitions}, err
		},
		BrowserFactory: func(participant room.Participant, static runtimePlanning.ToolCapabilities) (runtimePlanning.BrowserCapabilities, error) {
			if opts.BrowserCapabilitiesFactory == nil {
				return runtimePlanning.BrowserCapabilities{}, runtimePlanning.ErrParticipantBrowser
			}
			capabilities, err := opts.BrowserCapabilitiesFactory(participant)
			if err != nil {
				return runtimePlanning.BrowserCapabilities{}, err
			}
			composed, err := composeRoomParticipantBrowserCapabilities(participant, RoomParticipantToolCapabilities{Executor: static.Executor, Definitions: static.Definitions}, capabilities)
			if err != nil {
				if capabilities.Close != nil {
					_ = capabilities.Close()
				}
				return runtimePlanning.BrowserCapabilities{}, err
			}
			return runtimePlanning.BrowserCapabilities{Executor: composed.Executor, Definitions: composed.Definitions, ToolDefinitionBase: composed.ToolDefinitionBase, RefreshToolDefinitions: composed.RefreshToolDefinitions, BrowserWatch: composed.BrowserWatch, BrowserEventWatch: composed.BrowserEventWatch, Initialize: composed.Initialize, Close: composed.Close}, nil
		},
	}
	if usesProductionFactory {
		publicOptions.ResolveSampleRate = func(value runtimePlanning.SessionOptions, inferencer messages.SessionInferencer) (int, error) {
			local := roomSessionOptionsFromRuntime(opts, value, "")
			return resolveSessionAudioSampleRate(local, sessionRuntimePlan{provider: effectiveSessionProvider(local), inferencer: inferencer})
		}
	}
	if evidence != nil {
		publicOptions.ResolveCapturePath = func(id string) (string, bool) {
			participantEvidence := evidence.participant(id)
			if participantEvidence == nil || participantEvidence.artifacts.Capture == "" {
				return "", false
			}
			return filepath.Join(evidence.destination, participantEvidence.artifacts.Capture), true
		}
	}
	if opts.ReplayPlan != nil {
		replayPlan := runtimeReplayPlan(*opts.ReplayPlan)
		publicOptions.Manifest, publicOptions.ReplayPlan = replayPlan.Manifest(), &replayPlan
		publicOptions.ReplayPlanner = func(replayCtx context.Context, request runtimePlanning.ReplayRequest) (runtimePlanning.ReplaySession, error) {
			local, err := planSessionRuntime(roomSessionOptionsFromRuntime(opts, request.Options, ""))
			if err != nil {
				return runtimePlanning.ReplaySession{}, err
			}
			_ = replayCtx
			return runtimePlanning.ReplaySession{Inferencer: local.inferencer, Done: local.loop.Done, DoneErr: local.loop.DoneErr, MaxDuration: local.loop.MaxDuration}, nil
		}
	}
	result, err := runtimePlanningWire.NewService(runtimePlanningWire.Dependencies{}).Plan(ctx, publicOptions)
	for _, plan := range result.Plans {
		if plan != nil {
			plans = append(plans, localRoomParticipantPlan(opts, plan, credentialByID[plan.Participant.ID]))
		}
	}
	if opts.ReplayPlan != nil {
		return plans, nil, adaptRoomPlanningError(err)
	}
	return plans, roomSecretsInManifest(opts.Manifest, credentialByID), adaptRoomPlanningError(err)
}

func adaptRoomPlanningError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, runtimePlanning.ErrParticipantToolMatch):
		return fmt.Errorf("%w: %v", ErrRoomParticipantToolMismatch, err)
	case errors.Is(err, runtimePlanning.ErrParticipantTools):
		return fmt.Errorf("%w: %v", ErrRoomParticipantToolsUnavailable, err)
	case errors.Is(err, runtimePlanning.ErrParticipantBrowser):
		return fmt.Errorf("%w: %v", ErrRoomParticipantBrowserToolsUnavailable, err)
	default:
		return err
	}
}

func localRoomParticipantPlan(opts RoomRunOptions, plan *runtimePlanning.ParticipantPlan, credential string) *roomParticipantPlan {
	local := &roomParticipantPlan{manifest: plan.Participant, options: roomSessionOptionsFromRuntime(opts, plan.Options, credential), inferencer: plan.Inferencer, startupErr: plan.StartupErr, replay: plan.Replay, secret: credential, inputAudioSampleRate: plan.InputAudioSampleRate}
	if roomParticipantIsHumanAdapter(local) {
		local.options = SessionRunOptions{}
	}
	if plan.Options.CapabilityClose != nil {
		local.capabilityCoordinator = NewSessionCapabilityCoordinator(plan.Options.CapabilityClose)
		local.options.CapabilityClose = local.capabilityCoordinator.Close
	}
	if plan.Replay {
		local.replayLoop = sessionLoopOptions{Done: plan.ReplaySession.Done, DoneErr: plan.ReplaySession.DoneErr, MaxDuration: plan.ReplaySession.MaxDuration}
	}
	if local.inferencer != nil && !roomParticipantIsHumanAdapter(local) {
		local.tracker = newRoomConnectTrackingInferencer(local.inferencer)
	}
	return local
}

func roomSessionOptionsFromRuntime(parent RoomRunOptions, value runtimePlanning.SessionOptions, credential string) SessionRunOptions {
	local := SessionRunOptions{Provider: value.Provider, Model: value.Model, ModelProvided: value.ModelProvided, APIKey: credential, BaseURL: value.BaseURL, ConfigDir: value.ConfigDir, WorkDir: value.WorkDir, AllowPaths: append([]string(nil), value.AllowPaths...), FilesystemPolicy: parent.FilesystemPolicy, Prompt: value.Prompt, PromptProvided: value.PromptProvided, Voice: value.Voice, ReplayPath: value.ReplayPath, roomReplay: value.RoomReplay, WaitForClose: value.WaitForClose, WebSocketDialer: value.WebSocketDialer, ToolExecutor: value.ToolExecutor, ToolDefinitions: value.ToolDefinitions, ToolDefinitionBase: value.ToolDefinitionBase, RefreshToolDefinitions: value.RefreshTools, BrowserToolsEnabled: value.BrowserTools, RecordSessionCapturePath: value.RecordSessionCapturePath, CapabilityClose: value.CapabilityClose, Clock: parent.Clock, LivenessClock: parent.LivenessClock, ModelCatalog: parent.ModelCatalog}
	if value.RoomReplay {
		local.FilesystemPolicy, local.WorkDir, local.AllowPaths = nil, "", nil
	}
	if watch, ok := value.BrowserWatch.(func(context.Context) <-chan webmcp.BrokerEvent); ok {
		local.BrowserWatch = watch
	}
	if watch, ok := value.BrowserEventWatch.(func(context.Context) <-chan webmcp.BrowserEvent); ok {
		local.BrowserEventWatch = watch
	}
	return local
}

func awaitRoomParticipantConnectionsAdapter(ctx context.Context, coordinator *roomCoordinator, plans []*roomParticipantPlan, timer *time.Timer, secrets []string, outcomes <-chan roomConnectionOutcome, cleanup *roomCleanupWaiter) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if cleanup == nil {
		cleanup = &roomCleanupWaiter{}
	}
	done := make(chan struct{})
	defer close(done)
	bridged := make(chan runtimePlanning.ConnectionOutcome, len(plans))
	go bridgeRoomOutcomes(done, bridged, outcomes)
	return runtimePlanningWire.NewService(runtimePlanningWire.Dependencies{}).Await(ctx, runtimePlanning.AwaitOptions{Participants: roomAdmissionParticipants(plans), Outcomes: bridged, Timer: timerChannel(timer), Coordinator: roomPlanningCoordinator{inner: coordinator}, Cleanup: roomPlanningCleanup{inner: cleanup}, ParticipantError: func(id string, err error) error {
		return roomParticipantFailure(id, err, append(secretsForRoomPlan(id, plans), secrets...))
	}, LifecycleLabel: roomLifecycleWorkLabel, LifecycleError: newRoomLifecycleWorkError})
}

func bridgeRoomOutcomes(done chan struct{}, bridged chan<- runtimePlanning.ConnectionOutcome, outcomes <-chan roomConnectionOutcome) {
	for {
		select {
		case outcome, ok := <-outcomes:
			if !ok {
				return
			}
			select {
			case bridged <- runtimePlanning.ConnectionOutcome{Tracker: outcome.tracker, Err: outcome.err}:
			case <-done:
				return
			}
		case <-done:
			return
		}
	}
}

func roomAdmissionParticipants(plans []*roomParticipantPlan) []runtimePlanning.AdmissionParticipant {
	participants := make([]runtimePlanning.AdmissionParticipant, 0, len(plans))
	for _, plan := range plans {
		if plan != nil {
			participants = append(participants, roomAdmissionParticipant(plan))
		}
	}
	return participants
}
func roomAdmissionParticipant(plan *roomParticipantPlan) runtimePlanning.AdmissionParticipant {
	var tracker any
	if plan.tracker != nil {
		tracker = plan.tracker
	}
	return runtimePlanning.AdmissionParticipant{ID: plan.manifest.ID, Kind: plan.manifest.Kind, Tracker: tracker, StartupErr: plan.startupErr, MarkConnected: func(err error) {
		if plan.participant != nil && plan.participant.lifecycle != nil {
			plan.participant.lifecycle.markConnected(err)
		}
	}, Snapshot: func() runtimePlanning.LifecycleSnapshot { return roomLifecycleSnapshot(plan) }, OutstandingWork: func() []string {
		if plan.participant == nil {
			return nil
		}
		work := roomParticipantOutstandingWork(plan.participant)
		return work
	}}
}
func roomLifecycleSnapshot(plan *roomParticipantPlan) runtimePlanning.LifecycleSnapshot {
	if plan.participant == nil || plan.participant.lifecycle == nil {
		return runtimePlanning.LifecycleSnapshot{}
	}
	lifecycle := plan.participant.lifecycle
	_, opened, closed, _, _, _, _ := lifecycle.snapshot()
	_, terminalErr, terminalObserved := lifecycle.terminal()
	return runtimePlanning.LifecycleSnapshot{DeviceReady: lifecycle.deviceHasReady(), Opened: opened, Closed: closed, TransportEnded: lifecycle.transportHasEnded(), RunFinished: lifecycle.runHasFinished(), TerminalErr: terminalErr, TerminalObserved: terminalObserved}
}

type roomPlanningCoordinator struct{ inner *roomCoordinator }

func (c roomPlanningCoordinator) Done() <-chan struct{}     { return c.inner.done }
func (c roomPlanningCoordinator) Progress() <-chan struct{} { return c.inner.progress }
func (c roomPlanningCoordinator) IsActive(id string) bool   { return c.inner.isActive(id) }
func (c roomPlanningCoordinator) IsStopping() bool          { return c.inner.isStopping() }
func (c roomPlanningCoordinator) Stop(reason runtimePlanning.TerminationReason) {
	if reason == runtimePlanning.TerminationMaxDurationReached {
		c.inner.stop(RoomTerminationMaxDurationReached, nil)
		return
	}
	c.inner.stop(RoomTerminationStopped, nil)
}
func (c roomPlanningCoordinator) FailParticipant(id string, err error) {
	c.inner.failParticipant(id, err)
}
func (c roomPlanningCoordinator) Fail(err error)   { c.inner.fail(err) }
func (c roomPlanningCoordinator) RoomError() error { return c.inner.roomError() }

type roomPlanningCleanup struct{ inner *roomCleanupWaiter }

func (c roomPlanningCleanup) Start()                 { c.inner.start() }
func (c roomPlanningCleanup) Done() <-chan time.Time { return c.inner.done() }
func secretsForRoomPlan(id string, plans []*roomParticipantPlan) []string {
	for _, plan := range plans {
		if plan != nil && plan.manifest.ID == id {
			return secretsForPlan(plan)
		}
	}
	return nil
}
func roomSecretsInManifest(manifest room.Manifest, values map[string]string) []string {
	secrets := make([]string, 0, len(values))
	for _, participant := range manifest.Participants {
		if value := values[participant.ID]; value != "" {
			secrets = append(secrets, value)
		}
	}
	return secrets
}
func runtimeReplayPlan(local RoomReplayPlan) runtimeRooms.RoomReplayPlan {
	result := runtimeRooms.RoomReplayPlan{BundlePath: local.BundlePath, ManifestPath: local.ManifestPath, SchemaVersion: local.SchemaVersion, Finalized: local.Finalized, ClockBase: local.ClockBase, StartedAt: local.StartedAt, EndedAt: local.EndedAt, TimelinePath: local.TimelinePath, RoomMixPath: local.RoomMixPath}
	result.Participants = make([]runtimeRooms.RoomReplayParticipant, 0, len(local.Participants))
	for _, participant := range local.Participants {
		result.Participants = append(result.Participants, runtimeRooms.RoomReplayParticipant{ID: participant.ID, Kind: participant.Kind, Provider: participant.Provider, Model: participant.Model, Voice: participant.Voice, OpeningPrompt: participant.OpeningPrompt, SystemPrompt: participant.SystemPrompt, CapturePath: participant.CapturePath, RecordedTurnCount: participant.RecordedTurnCount})
	}
	return result
}
