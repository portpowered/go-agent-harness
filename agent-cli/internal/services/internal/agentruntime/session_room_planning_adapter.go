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

type roomPlanningAdapterState struct {
	options               RoomRunOptions
	policy                *tools.FilesystemPolicy
	credentials           *roomCredentialLookup
	evidence              *roomEvidence
	sessionFactory        func(room.Participant, SessionRunOptions) (messages.SessionInferencer, error)
	toolFactory           RoomParticipantToolCapabilitiesFactory
	usesProductionFactory bool
}

type roomCredentialLookup struct {
	lookup   func(string) (string, bool)
	idsByEnv map[string][]string
	values   map[string]string
	ok       map[string]bool
	byID     map[string]string
}

func buildRoomParticipantPlansAdapter(ctx context.Context, opts RoomRunOptions, validation room.ValidationOptions, evidences ...*roomEvidence) (plans []*roomParticipantPlan, secrets []string, planErr error) {
	policy, err := resolveRoomPlanningPolicy(opts)
	if err != nil {
		return nil, nil, err
	}
	opts.FilesystemPolicy, opts.WorkDir, opts.AllowPaths = policy, policy.PrimaryRoot(), policy.AdditionalRoots()
	state, err := newRoomPlanningAdapterState(ctx, opts, validation, policy, evidences...)
	if err != nil {
		return nil, roomSecretsInManifest(opts.Manifest, stateCredentialValues(state)), err
	}
	publicOptions := state.publicOptions()
	state.configureSampleRate(&publicOptions)
	state.configureCapture(&publicOptions)
	state.configureReplay(&publicOptions)
	result, err := runtimePlanningWire.NewService(runtimePlanningWire.Dependencies{}).Plan(ctx, publicOptions)
	for _, plan := range result.Plans {
		if plan != nil {
			plans = append(plans, localRoomParticipantPlan(opts, plan, state.credentials.byID[plan.Participant.ID]))
		}
	}
	if opts.ReplayPlan != nil {
		return plans, nil, adaptRoomPlanningError(err)
	}
	return plans, state.secrets(), adaptRoomPlanningError(err)
}
func resolveRoomPlanningPolicy(opts RoomRunOptions) (*tools.FilesystemPolicy, error) {
	if opts.FilesystemPolicy != nil {
		return opts.FilesystemPolicy, nil
	}
	policy, err := tools.ResolveFilesystemPolicy(opts.WorkDir, opts.AllowPaths...)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem scope: %w", err)
	}
	return policy, nil
}
func newRoomPlanningAdapterState(ctx context.Context, opts RoomRunOptions, validation room.ValidationOptions, policy *tools.FilesystemPolicy, evidences ...*roomEvidence) (*roomPlanningAdapterState, error) {
	credentials := newRoomCredentialLookup(opts, validation)
	if opts.ReplayPlan == nil {
		credentials.prime(opts.Manifest)
	}
	sessionFactory := opts.SessionFactory
	if sessionFactory == nil {
		sessionFactory = defaultRoomSessionFactory
	}
	toolFactory := opts.ToolCapabilitiesFactory
	var evidence *roomEvidence
	if len(evidences) > 0 {
		evidence = evidences[0]
	}
	state := &roomPlanningAdapterState{options: opts, policy: policy, credentials: credentials, evidence: evidence, sessionFactory: sessionFactory, usesProductionFactory: opts.SessionFactory == nil && opts.WebSocketDialerFactory == nil}
	if toolFactory == nil && roomManifestHasTools(opts.Manifest) {
		var err error
		toolFactory, err = newDefaultRoomParticipantToolCapabilitiesFactoryWithPolicy(ctx, opts.ConfigDir, policy)
		if err != nil {
			return state, fmt.Errorf("%w: %w", ErrRoomParticipantToolsUnavailable, err)
		}
	}
	state.toolFactory = toolFactory
	return state, nil
}
func newRoomCredentialLookup(opts RoomRunOptions, validation room.ValidationOptions) *roomCredentialLookup {
	lookup := validation.LookupCredential
	if lookup == nil {
		lookup = opts.CredentialLookup
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	result := &roomCredentialLookup{lookup: lookup, idsByEnv: map[string][]string{}, values: map[string]string{}, ok: map[string]bool{}, byID: map[string]string{}}
	for _, participant := range opts.Manifest.Participants {
		if participant.APIKeyEnv != "" {
			result.idsByEnv[participant.APIKeyEnv] = append(result.idsByEnv[participant.APIKeyEnv], participant.ID)
		}
	}
	return result
}
func (lookup *roomCredentialLookup) prime(manifest room.Manifest) {
	for _, participant := range manifest.Participants {
		if participant.APIKeyEnv != "" {
			_, _ = lookup.record(participant.APIKeyEnv)
		}
	}
}
func (lookup *roomCredentialLookup) record(name string) (string, bool) {
	if _, lookedUp := lookup.values[name]; lookedUp {
		return lookup.values[name], lookup.ok[name]
	}
	value, ok := lookup.lookup(name)
	lookup.values[name], lookup.ok[name] = value, ok
	if ok && value != "" {
		for _, id := range lookup.idsByEnv[name] {
			lookup.byID[id] = value
		}
	}
	return value, ok
}
func (s *roomPlanningAdapterState) publicOptions() runtimePlanning.Options {
	return runtimePlanning.Options{
		Manifest: s.options.Manifest, LookupCredential: s.credentials.record,
		Filesystem: &runtimePlanning.FilesystemScope{PrimaryRoot: s.policy.PrimaryRoot(), AdditionalRoots: s.policy.AdditionalRoots()},
		WorkDir:    s.options.WorkDir, AllowPaths: s.options.AllowPaths, ConfigDir: s.options.ConfigDir, BaseURL: s.options.BaseURL,
		WebSocketDialer: s.options.WebSocketDialer, WebSocketDialerFactory: s.options.WebSocketDialerFactory,
		ParticipantError: s.participantError, SessionInferencers: s.options.SessionInferencers,
		SessionFactory: s.sessionFactoryAdapter, ToolFactory: s.toolFactoryAdapter, BrowserFactory: s.browserFactoryAdapter,
	}
}
func (s *roomPlanningAdapterState) participantError(id string, err error) error {
	return roomParticipantFailure(id, err, []string{s.credentials.byID[id]})
}
func (s *roomPlanningAdapterState) sessionFactoryAdapter(request runtimePlanning.LiveSessionRequest) (messages.SessionInferencer, error) {
	options := roomSessionOptionsFromRuntime(s.options, request.Options, request.Credential)
	return s.sessionFactory(request.Participant, options)
}
func (s *roomPlanningAdapterState) toolFactoryAdapter(participant room.Participant) (runtimePlanning.ToolCapabilities, error) {
	if s.toolFactory == nil {
		return runtimePlanning.ToolCapabilities{}, runtimePlanning.ErrParticipantTools
	}
	capabilities, err := s.toolFactory(participant)
	return runtimePlanning.ToolCapabilities{Executor: capabilities.Executor, Definitions: capabilities.Definitions}, err
}
func (s *roomPlanningAdapterState) browserFactoryAdapter(participant room.Participant, static runtimePlanning.ToolCapabilities) (runtimePlanning.BrowserCapabilities, error) {
	if s.options.BrowserCapabilitiesFactory == nil {
		return runtimePlanning.BrowserCapabilities{}, runtimePlanning.ErrParticipantBrowser
	}
	capabilities, err := s.options.BrowserCapabilitiesFactory(participant)
	if err != nil {
		return runtimePlanning.BrowserCapabilities{}, err
	}
	composed, err := composeRoomParticipantBrowserCapabilities(participant, RoomParticipantToolCapabilities{Executor: static.Executor, Definitions: static.Definitions}, capabilities)
	if err != nil {
		if closeErr := capabilities.Close(); closeErr != nil {
			return runtimePlanning.BrowserCapabilities{}, errors.Join(err, closeErr)
		}
		return runtimePlanning.BrowserCapabilities{}, err
	}
	return runtimePlanning.BrowserCapabilities{Executor: composed.Executor, Definitions: composed.Definitions, ToolDefinitionBase: composed.ToolDefinitionBase, RefreshToolDefinitions: composed.RefreshToolDefinitions, BrowserWatch: composed.BrowserWatch, BrowserEventWatch: composed.BrowserEventWatch, Initialize: composed.Initialize, Close: composed.Close}, nil
}
func (s *roomPlanningAdapterState) configureSampleRate(options *runtimePlanning.Options) {
	if !s.usesProductionFactory {
		return
	}
	options.ResolveSampleRate = func(value runtimePlanning.SessionOptions, inferencer messages.SessionInferencer) (int, error) {
		local := roomSessionOptionsFromRuntime(s.options, value, "")
		return resolveSessionAudioSampleRate(local, sessionRuntimePlan{provider: effectiveSessionProvider(local), inferencer: inferencer})
	}
}
func (s *roomPlanningAdapterState) configureCapture(options *runtimePlanning.Options) {
	if s.evidence == nil {
		return
	}
	options.ResolveCapturePath = s.capturePath
}
func (s *roomPlanningAdapterState) capturePath(id string) (string, bool) {
	participantEvidence := s.evidence.participant(id)
	if participantEvidence == nil || participantEvidence.artifacts.Capture == "" {
		return "", false
	}
	return filepath.Join(s.evidence.destination, participantEvidence.artifacts.Capture), true
}
func (s *roomPlanningAdapterState) configureReplay(options *runtimePlanning.Options) {
	if s.options.ReplayPlan == nil {
		return
	}
	replayPlan := runtimeReplayPlan(*s.options.ReplayPlan)
	options.Manifest, options.ReplayPlan = replayPlan.Manifest(), &replayPlan
	options.ReplayPlanner = s.replayPlanner
}
func (s *roomPlanningAdapterState) replayPlanner(_ context.Context, request runtimePlanning.ReplayRequest) (runtimePlanning.ReplaySession, error) {
	//nolint:contextcheck // replay planning cannot pass context through the legacy private contract.
	local, err := planSessionRuntime(roomSessionOptionsFromRuntime(s.options, request.Options, ""))
	if err != nil {
		return runtimePlanning.ReplaySession{}, err
	}
	return runtimePlanning.ReplaySession{Inferencer: local.inferencer, Done: local.loop.Done, DoneErr: local.loop.DoneErr, MaxDuration: local.loop.MaxDuration}, nil
}

func stateCredentialValues(state *roomPlanningAdapterState) map[string]string {
	if state == nil || state.credentials == nil {
		return nil
	}
	return state.credentials.byID
}

func (s *roomPlanningAdapterState) secrets() []string {
	return roomSecretsInManifest(s.options.Manifest, s.credentials.byID)
}

func adaptRoomPlanningError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, runtimePlanning.ErrParticipantToolMatch):
		return fmt.Errorf("%w: %w", ErrRoomParticipantToolMismatch, err)
	case errors.Is(err, runtimePlanning.ErrParticipantTools):
		return fmt.Errorf("%w: %w", ErrRoomParticipantToolsUnavailable, err)
	case errors.Is(err, runtimePlanning.ErrParticipantBrowser):
		return fmt.Errorf("%w: %w", ErrRoomParticipantBrowserToolsUnavailable, err)
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
	_, opened, closed, _, _, _, connectErr := lifecycle.snapshot()
	_, terminalErr, terminalObserved := lifecycle.terminal()
	if terminalErr == nil && connectErr != nil {
		terminalErr, terminalObserved = connectErr, true
	}
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
