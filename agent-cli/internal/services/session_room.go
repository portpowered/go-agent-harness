package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// RoomTerminationReason is the room-level terminal taxonomy. A room has one
// reason even when individual participants finish at different times.
type RoomTerminationReason string

const (
	// DefaultRoomOutputDir is the deterministic evidence directory used by the
	// room CLI when --out is omitted. It is resolved relative to the process's
	// working directory and must still satisfy the empty-directory safety check.
	DefaultRoomOutputDir = "room-run"

	RoomTerminationStopped            RoomTerminationReason = "stopped"
	RoomTerminationMaxTurnsReached    RoomTerminationReason = "max_turns_reached"
	RoomTerminationMaxDurationReached RoomTerminationReason = "max_duration_reached"
	RoomTerminationFailed             RoomTerminationReason = "failed"
)

// RoomStopReason is a descriptive alias used by callers that name the room
// terminal state a stop reason.
type RoomStopReason = RoomTerminationReason

const (
	RoomStopStopped            = RoomTerminationStopped
	RoomStopMaxTurnsReached    = RoomTerminationMaxTurnsReached
	RoomStopMaxDurationReached = RoomTerminationMaxDurationReached
	RoomStopFailed             = RoomTerminationFailed
	RoomStopped                = RoomTerminationStopped
	RoomMaxTurnsReached        = RoomTerminationMaxTurnsReached
	RoomMaxDurationReached     = RoomTerminationMaxDurationReached
	RoomFailed                 = RoomTerminationFailed
)

// ParticipantTerminationReason is the participant-level terminal taxonomy.
// It intentionally remains independent of the room reason.
type ParticipantTerminationReason string

const (
	ParticipantTerminationEnded        ParticipantTerminationReason = "ended"
	ParticipantTerminationDisconnected ParticipantTerminationReason = "disconnected"
	ParticipantTerminationError        ParticipantTerminationReason = "error"
)

// RoomParticipantResult contains the observable outcome for one participant.
// Error is already sanitized; the resolved API-key value is never retained in
// the result.
type RoomParticipantResult struct {
	// ID and TerminationReason are the joined run-manifest names. The
	// ParticipantID and Reason aliases keep the result convenient for runtime
	// callers that use the same terminology as RoomParticipantEvent.
	ID                string                       `json:"id"`
	ParticipantID     string                       `json:"participant_id,omitempty"`
	TerminationReason ParticipantTerminationReason `json:"termination_reason"`
	Reason            ParticipantTerminationReason `json:"reason,omitempty"`
	TurnsCompleted    int                          `json:"turns_completed"`
	Connected         bool                         `json:"connected"`
	Error             string                       `json:"error,omitempty"`
}

// RoomResult contains the room outcome and every participant outcome. The map
// is keyed by the manifest's stable participant ID.
type RoomResult struct {
	TerminationReason  RoomTerminationReason            `json:"termination_reason"`
	Reason             RoomTerminationReason            `json:"reason,omitempty"`
	Participants       map[string]RoomParticipantResult `json:"participants"`
	ActiveParticipants []string                         `json:"active_participants,omitempty"`
	Error              string                           `json:"error,omitempty"`
}

// RoomRunResult is the descriptive result name used by callers that model a
// room execution as a value rather than a generic room state.
type RoomRunResult = RoomResult

// RoomSessionInferencerFactory constructs one independently configured
// participant session. The manifest participant contains only credential
// metadata; the resolved credential is available only in sessionOptions.
type RoomSessionInferencerFactory func(room.Participant, SessionRunOptions) (messages.SessionInferencer, error)

// RoomParticipantSessionFactory is the explicit composition-root name for
// RoomSessionInferencerFactory.
type RoomParticipantSessionFactory = RoomSessionInferencerFactory

// RoomSessionFactory is a concise alias for the participant factory.
type RoomSessionFactory = RoomSessionInferencerFactory

// RoomParticipantAudioObserver receives a copied provider AUDIO.DELTA before
// it is fanned into the other participants' mixers. It is observational and
// may be used by the evidence writer in a later composition layer.
type RoomParticipantAudioObserver func(participantID string, pcm []byte) error

// RoomParticipantDiagnosticObserver receives the credential-free diagnostic
// projection for one participant. It is intended for bounded terminal
// progress; raw stream deltas and audio remain unavailable through this seam.
type RoomParticipantDiagnosticObserver func(participantID string, record SessionDiagnosticRecord)

// RoomParticipantObserver receives one event after a participant leaves the
// room. It is called only after that participant's own mixer has been stopped.
type RoomParticipantObserver func(RoomParticipantResult)

// RoomObserver receives the single room terminal event after all participant
// goroutines and mixers have been torn down.
type RoomObserver func(RoomResult)

// RoomRunOptions configures a manifest-defined N-participant room. A custom
// SessionFactory or SessionInferencers map is intended for deterministic tests;
// the default factory builds the repository's existing live session runtime.
type RoomRunOptions struct {
	Manifest room.Manifest
	// OutputDir enables the durable room evidence bundle. An empty value keeps
	// the service's observational-only mode for callers that do not need
	// artifacts; the room CLI supplies a concrete, empty directory.
	OutputDir string

	SessionFactory     RoomSessionInferencerFactory
	SessionInferencers map[string]messages.SessionInferencer
	// ToolCapabilitiesFactory supplies an isolated tool executor and matching
	// provider definitions for each participant that names tools. A nil value
	// uses the normal config-backed registry when tools are requested; an
	// explicit empty tools list never constructs or advertises tools.
	ToolCapabilitiesFactory RoomParticipantToolCapabilitiesFactory

	// Validation is applied before any session factory is called. Setting
	// CredentialLookup is a convenience override for Validation.LookupCredential.
	Validation       room.ValidationOptions
	CredentialLookup func(string) (string, bool)

	PairFactory            room.PairFactory
	BaseURL                string
	ConfigDir              string
	WebSocketDialer        transport.Dialer
	WebSocketDialerFactory func(room.Participant) transport.Dialer
	// FrameSamples is a compact deterministic cadence override. Zero leaves
	// PCMFormat/MixerConfig unchanged; otherwise it uses the default 24 kHz
	// mono format with this many samples per frame.
	FrameSamples int

	// MixerConfig defaults to the room PCM16 contract when zero. PCMFormat is
	// retained as a concise override for callers that only need to change the
	// format and not queue limits.
	MixerConfig room.PCM16MixerConfig
	PCMFormat   room.PCM16Format

	OnAudioOutput           RoomParticipantAudioObserver
	OnAudioInput            RoomParticipantAudioObserver
	OnDiagnostic            RoomParticipantDiagnosticObserver
	OnParticipantTerminated RoomParticipantObserver
	OnRoomTerminated        RoomObserver
	// Stream optionally receives the room's diagnostic, transcript, and
	// lifecycle projections. The broker is observational and never carries raw
	// audio. Callers that expose it over HTTP own the listener lifecycle.
	Stream *RoomEventBroker
}

// RoomOptions is a concise alias for RoomRunOptions.
type RoomOptions = RoomRunOptions

type roomParticipantPlan struct {
	manifest    room.Participant
	options     SessionRunOptions
	inferencer  messages.SessionInferencer
	secret      string
	tracker     *roomConnectTrackingInferencer
	participant *roomParticipantRuntime
}

type roomParticipantRuntime struct {
	plan      *roomParticipantPlan
	ctx       context.Context
	cancel    context.CancelFunc
	loopReady chan *agentloop.AgentLoop
	mixer     *room.PCM16Mixer
	lifecycle *roomParticipantLifecycle
}

type roomParticipantLifecycle struct {
	mu             sync.Mutex
	connected      bool
	connectErr     error
	sessionOpened  bool
	sessionClosed  bool
	transportEnded bool
	transportDone  <-chan struct{}
	runDone        bool
	closeReason    string
	terminalReason messages.TerminalReason
	turns          int
}

func (l *roomParticipantLifecycle) markConnected(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.connected = err == nil
	l.connectErr = err
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) observe(msg messages.StreamMessage) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch msg.Type {
	case messages.StreamTypeSessionOpen:
		l.sessionOpened = true
	case messages.StreamTypeMessageEnd:
		l.turns++
	case messages.StreamTypeSessionClose:
		l.sessionClosed = true
		if value, ok := msg.Value.(*messages.SessionCloseValue); ok && value != nil {
			l.closeReason = value.Reason
			l.terminalReason = value.TerminalReason
		}
	}
	return l.turns
}

func (l *roomParticipantLifecycle) markTransportEnded() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transportEnded = true
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) setTransportDone(done <-chan struct{}) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transportDone = done
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) transportHasEnded() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	ended := l.transportEnded
	done := l.transportDone
	l.mu.Unlock()
	if ended || done == nil {
		return ended
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (l *roomParticipantLifecycle) markRunDone() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.runDone = true
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) runHasFinished() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runDone
}

func (l *roomParticipantLifecycle) snapshot() (connected bool, connectErr error, sessionOpened bool, sessionClosed bool, closeReason string, terminalReason messages.TerminalReason, turns int) {
	if l == nil {
		return false, nil, false, false, "", "", 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connected, l.connectErr, l.sessionOpened, l.sessionClosed, l.closeReason, l.terminalReason, l.turns
}

// roomConnectTrackingInferencer preserves the existing SessionInferencer
// contract while exposing the first ConnectSession outcome to the room's
// initial-start barrier.
type roomConnectTrackingInferencer struct {
	inner      messages.SessionInferencer
	result     chan error
	once       sync.Once
	mu         sync.Mutex
	ready      bool
	connectErr error
	lifecycle  *roomParticipantLifecycle
}

func newRoomConnectTrackingInferencer(inner messages.SessionInferencer) *roomConnectTrackingInferencer {
	return &roomConnectTrackingInferencer{inner: inner, result: make(chan error, 1)}
}

func (i *roomConnectTrackingInferencer) publish(err error) {
	if i == nil {
		return
	}
	i.once.Do(func() {
		i.mu.Lock()
		i.ready = true
		i.connectErr = err
		i.mu.Unlock()
		i.result <- err
	})
}

func (i *roomConnectTrackingInferencer) outcome() (error, bool) {
	if i == nil {
		return nil, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectErr, i.ready
}

func (i *roomConnectTrackingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		err := errors.New("room participant session inferencer is nil")
		if i != nil {
			i.publish(err)
		}
		return nil, err
	}
	session, err := i.inner.ConnectSession(ctx)
	if err == nil && session == nil {
		err = errors.New("room participant session is nil")
	}
	i.publish(err)
	if err == nil && session != nil && i.lifecycle != nil {
		i.lifecycle.setTransportDone(session.Done())
		go func() {
			select {
			case <-session.Done():
				i.lifecycle.markTransportEnded()
			case <-ctx.Done():
			}
		}()
	}
	return session, err
}

var _ messages.SessionInferencer = (*roomConnectTrackingInferencer)(nil)

type roomCoordinator struct {
	done   chan struct{}
	cancel context.CancelFunc

	mu       sync.Mutex
	reason   RoomTerminationReason
	err      error
	active   map[string]*roomParticipantRuntime
	results  map[string]RoomParticipantResult
	maxTurns int

	onParticipant RoomParticipantObserver
}

func newRoomCoordinator(cancel context.CancelFunc, maxTurns int, onParticipant RoomParticipantObserver) *roomCoordinator {
	return &roomCoordinator{
		done:          make(chan struct{}),
		cancel:        cancel,
		active:        make(map[string]*roomParticipantRuntime),
		results:       make(map[string]RoomParticipantResult),
		maxTurns:      maxTurns,
		onParticipant: onParticipant,
	}
}

func (c *roomCoordinator) stop(reason RoomTerminationReason, err error) {
	if c == nil {
		return
	}
	if reason == "" {
		reason = RoomTerminationFailed
	}
	c.mu.Lock()
	if c.reason != "" {
		c.mu.Unlock()
		return
	}
	c.reason = reason
	c.err = err
	c.mu.Unlock()
	close(c.done)
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *roomCoordinator) fail(err error) {
	if err == nil {
		err = errors.New("room failed")
	}
	c.stop(RoomTerminationFailed, err)
}

func (c *roomCoordinator) failedParticipantID() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var safe *roomSafeError
	if errors.As(c.err, &safe) {
		return safe.participantID
	}
	return ""
}

func (c *roomCoordinator) isStopping() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason != ""
}

func (c *roomCoordinator) roomError() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *roomCoordinator) addParticipant(runtime *roomParticipantRuntime) {
	if c == nil || runtime == nil || runtime.plan == nil {
		return
	}
	c.mu.Lock()
	c.active[runtime.plan.manifest.ID] = runtime
	c.mu.Unlock()
}

func (c *roomCoordinator) activeExcept(participantID string) []*roomParticipantRuntime {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	result := make([]*roomParticipantRuntime, 0, len(c.active))
	for id, runtime := range c.active {
		if id != participantID {
			result = append(result, runtime)
		}
	}
	c.mu.Unlock()
	return result
}

func (c *roomCoordinator) isActive(participantID string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.active[participantID]
	return ok
}

func (c *roomCoordinator) noteTurn(participantID string, turns int) {
	if c == nil || c.maxTurns <= 0 {
		return
	}
	_ = turns
	c.mu.Lock()
	_, active := c.active[participantID]
	if !active || c.reason != "" {
		c.mu.Unlock()
		return
	}
	participants := make([]*roomParticipantRuntime, 0, len(c.active))
	for _, participant := range c.active {
		participants = append(participants, participant)
	}
	c.mu.Unlock()
	for _, participant := range participants {
		if participant == nil || participant.lifecycle == nil {
			return
		}
		_, _, _, _, _, _, completed := participant.lifecycle.snapshot()
		if completed < c.maxTurns {
			return
		}
	}
	c.mu.Lock()
	if c.reason != "" || len(c.active) != len(participants) {
		c.mu.Unlock()
		return
	}
	for _, participant := range participants {
		if participant == nil || participant.plan == nil {
			c.mu.Unlock()
			return
		}
		if _, ok := c.active[participant.plan.manifest.ID]; !ok {
			c.mu.Unlock()
			return
		}
	}
	c.mu.Unlock()
	c.stop(RoomTerminationMaxTurnsReached, nil)
}

func (c *roomCoordinator) finishParticipant(runtime *roomParticipantRuntime, reason ParticipantTerminationReason, err error, secrets []string, mesh *room.Mesh) RoomParticipantResult {
	if runtime == nil || runtime.plan == nil {
		return RoomParticipantResult{Reason: ParticipantTerminationError, Error: "room participant runtime is nil"}
	}
	id := runtime.plan.manifest.ID
	connected, connectErr, _, sessionClosed, closeReason, terminalReason, turns := runtime.lifecycle.snapshot()
	transportEnded := runtime.lifecycle.transportHasEnded()
	if connectErr != nil && err == nil {
		err = connectErr
	}
	// A room-level failure is returned by every session loop through DoneErr.
	// Keep it on the participant that caused the failure, but do not turn the
	// coordinator's cancellation into an error for surviving participants.
	if err != nil && c.isStopping() {
		if roomErr := c.roomError(); roomErr != nil {
			if failedID := c.failedParticipantID(); failedID != "" && failedID != id && errors.Is(err, roomErr) {
				err = nil
			}
		}
	}
	if reason == "" {
		reason = classifyRoomParticipantTermination(c.isStopping(), err, connected, transportEnded, sessionClosed, closeReason, terminalReason)
	}
	result := RoomParticipantResult{
		ID:                id,
		ParticipantID:     id,
		TerminationReason: reason,
		Reason:            reason,
		TurnsCompleted:    turns,
		Connected:         connected,
		Error:             sanitizeRoomError(err, secrets),
	}

	c.mu.Lock()
	if _, alreadyFinished := c.results[id]; alreadyFinished {
		previous := c.results[id]
		c.mu.Unlock()
		return previous
	}
	c.results[id] = result
	delete(c.active, id)
	shouldFailEmpty := len(c.active) == 0 && c.reason == ""
	c.mu.Unlock()

	// Remove the source from every surviving inbound mixer before closing its
	// own mixer. This discards only stale source bytes and keeps survivors live.
	for _, survivor := range c.activeExcept(id) {
		if survivor.mixer != nil {
			if removeErr := survivor.mixer.RemoveInput(id); removeErr != nil && !errors.Is(removeErr, room.ErrMixerInputMissing) && !errors.Is(removeErr, room.ErrMixerClosed) {
				c.fail(roomParticipantFailure(id, removeErr, secrets))
			}
		}
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	if runtime.mixer != nil {
		_ = runtime.mixer.Close()
	}
	if mesh != nil {
		if removeErr := mesh.Remove(id); removeErr != nil && !errors.Is(removeErr, room.ErrMeshUnknownParticipant) && !errors.Is(removeErr, room.ErrMeshClosed) {
			c.fail(roomParticipantFailure(id, removeErr, secrets))
		}
	}
	if c.onParticipant != nil {
		c.onParticipant(result)
	}
	if shouldFailEmpty {
		c.fail(fmt.Errorf("all room participants terminated"))
	}
	return result
}

func (c *roomCoordinator) snapshot() (RoomTerminationReason, error, map[string]RoomParticipantResult, []string) {
	if c == nil {
		return RoomTerminationFailed, errors.New("room coordinator is nil"), nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	results := make(map[string]RoomParticipantResult, len(c.results)+len(c.active))
	for id, result := range c.results {
		results[id] = result
	}
	active := make([]string, 0, len(c.active))
	for id := range c.active {
		active = append(active, id)
	}
	// The caller sorts active IDs to keep result assembly deterministic while
	// avoiding a sort while the coordinator lock is held.
	return c.reason, c.err, results, active
}

func classifyRoomParticipantTermination(roomStopping bool, runErr error, connected bool, transportEnded bool, sessionClosed bool, closeReason string, terminalReason messages.TerminalReason) ParticipantTerminationReason {
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
		return ParticipantTerminationError
	}
	if closeReason == "provider_closed" || terminalReason == messages.TerminalReasonProviderClose {
		return ParticipantTerminationDisconnected
	}
	if transportEnded && !sessionClosed && !roomStopping {
		return ParticipantTerminationDisconnected
	}
	if !connected && runErr != nil && !roomStopping {
		return ParticipantTerminationError
	}
	// A caller/bound/coordinator stop is an intentional clean participant
	// teardown. It must not be mistaken for a provider disconnect.
	return ParticipantTerminationEnded
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

	plans, secrets, err := buildRoomParticipantPlans(opts, validation)
	if err != nil {
		result := roomFailureResult(err, secrets)
		return finalizeEvidence(result, err)
	}

	roomCtx, roomCancel := context.WithCancel(ctx)
	defer roomCancel()
	mesh := room.NewParticipantMesh(roomCtx, opts.PairFactory)
	defer func() { _ = mesh.Close() }()
	for _, plan := range plans {
		if err := mesh.Join(roomCtx, plan.manifest.ID); err != nil {
			safeErr := roomParticipantFailure(plan.manifest.ID, err, secrets)
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
			roomErr := coordinator.roomError()
			result := roomFailureResult(roomErr, secrets)
			return finalizeEvidence(result, roomErr)
		}
		runtime := &roomParticipantRuntime{
			plan:      plan,
			ctx:       participantCtx,
			cancel:    participantCancel,
			loopReady: make(chan *agentloop.AgentLoop, 1),
			mixer:     mixer,
			lifecycle: &roomParticipantLifecycle{},
		}
		plan.participant = runtime
		plan.tracker.lifecycle = runtime.lifecycle
		for _, other := range plans {
			if other.manifest.ID == plan.manifest.ID {
				continue
			}
			if addErr := mixer.AddInput(other.manifest.ID); addErr != nil {
				coordinator.fail(roomParticipantFailure(plan.manifest.ID, addErr, secrets))
				_ = mixer.Close()
				participantCancel()
				roomErr := coordinator.roomError()
				result := roomFailureResult(roomErr, secrets)
				return finalizeEvidence(result, roomErr)
			}
		}
		coordinator.addParticipant(runtime)
	}

	// The room context inherits caller cancellation, but cancellation is
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
	var runWG sync.WaitGroup
	runWG.Add(len(plans))
	for _, plan := range plans {
		runtime := plan.participant
		go func(plan *roomParticipantPlan, runtime *roomParticipantRuntime) {
			defer runWG.Done()
			go pumpRoomMixer(roomCtx, coordinator, runtime, startGate, opts.OnAudioInput, secrets)
			participantEvidence := (*roomParticipantEvidence)(nil)
			if evidence != nil {
				participantEvidence = evidence.participant(plan.manifest.ID)
			}
			participantStream := RoomParticipantEventSink{}
			if opts.Stream != nil {
				participantStream = opts.Stream.ParticipantSink(plan.manifest.ID)
			}
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
			observer := newSessionProgressObserver(combineRoomDiagnosticSinks(diagnosticSinks...), nil, plan.manifest.Provider, plan.manifest.Model)
			observer.streamObserver = func(msg messages.StreamMessage) {
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
					if writeErr := target.mixer.Write(plan.manifest.ID, pcm); writeErr != nil && coordinator.isActive(target.plan.manifest.ID) {
						coordinator.fail(roomParticipantFailure(plan.manifest.ID, fmt.Errorf("fan out PCM to %s: %w", target.plan.manifest.ID, writeErr), secretsForPlan(plan)))
					}
				}
			}
			runErr := runAgentLoopSession(runtime.ctx, io.Discard, runtime.plan.tracker, sessionLoopOptions{
				WaitForClose:    true,
				Done:            coordinator.done,
				DoneErr:         coordinator.roomError,
				ToolExecutor:    plan.options.ToolExecutor,
				ToolDefinitions: cloneRoomToolDefinitions(plan.options.ToolDefinitions),
				observer:        observer,
				loopReady:       runtime.loopReady,
			})
			runtime.lifecycle.markRunDone()
			connected, connectErr, _, _, _, _, _ := runtime.lifecycle.snapshot()
			if trackedErr, ready := runtime.plan.tracker.outcome(); ready {
				connectErr = trackedErr
				runtime.lifecycle.markConnected(connectErr)
				connected = connectErr == nil
			}
			results <- roomParticipantRunResult{plan: plan, runtime: runtime, err: runErr, connected: connected, connectErr: connectErr}
		}(plan, runtime)
	}

	// A room duration bounds the initial connection phase as well as the live
	// conversation. The timer is stopped after every participant has returned.
	var timer *time.Timer
	if opts.Manifest.Room.MaxDuration > 0 {
		timer = time.NewTimer(opts.Manifest.Room.MaxDuration)
		defer timer.Stop()
	}

	startupErr := awaitRoomParticipantConnections(roomCtx, coordinator, plans, timer, secrets)
	if startupErr != nil {
		coordinator.fail(startupErr)
	}
	close(startGate)
	remaining := len(plans)
	for remaining > 0 {
		select {
		case <-timerChannel(timer):
			coordinator.stop(RoomTerminationMaxDurationReached, nil)
		case result := <-results:
			remaining--
			if result.connectErr != nil && !coordinator.isStopping() {
				coordinator.fail(roomParticipantFailure(result.plan.manifest.ID, result.connectErr, secretsForPlan(result.plan)))
			}
			finishRoomParticipant(coordinator, mesh, result, secretsForPlan(result.plan))
		}
	}
	runWG.Wait()

	reason, roomErr, participantResults, active := coordinator.snapshot()
	if reason == "" {
		coordinator.fail(errors.New("room ended without a terminal reason"))
		reason, roomErr, participantResults, active = coordinator.snapshot()
	}
	if meshErr := mesh.Close(); meshErr != nil {
		roomErr = errors.Join(roomErr, meshErr)
	}
	for _, plan := range plans {
		if _, ok := participantResults[plan.manifest.ID]; ok {
			continue
		}
		connected, connectErr, _, sessionClosed, closeReason, terminalReason, turns := plan.participant.lifecycle.snapshot()
		participantResults[plan.manifest.ID] = RoomParticipantResult{
			ID:                plan.manifest.ID,
			ParticipantID:     plan.manifest.ID,
			TerminationReason: classifyRoomParticipantTermination(true, connectErr, connected, plan.participant.lifecycle.transportHasEnded(), sessionClosed, closeReason, terminalReason),
			Reason:            classifyRoomParticipantTermination(true, connectErr, connected, plan.participant.lifecycle.transportHasEnded(), sessionClosed, closeReason, terminalReason),
			TurnsCompleted:    turns,
			Connected:         connected,
			Error:             sanitizeRoomError(connectErr, secretsForPlan(plan)),
		}
	}
	result := RoomResult{TerminationReason: reason, Reason: reason, Participants: participantResults, ActiveParticipants: append([]string(nil), active...)}
	if roomErr != nil {
		result.Error = sanitizeRoomError(roomErr, secrets)
	}
	result.ActiveParticipants = sortedRoomIDs(result.ActiveParticipants)
	result, roomErr = finalizeEvidence(result, roomErr)
	if opts.OnRoomTerminated != nil {
		opts.OnRoomTerminated(result)
	}
	if _, writeErr := fmt.Fprintf(out, "room stopped: reason=%s participants=%d\n", result.Reason, len(result.Participants)); writeErr != nil {
		roomErr = errors.Join(roomErr, fmt.Errorf("write room result: %w", writeErr))
	}
	return result, roomErr
}

type roomParticipantRunResult struct {
	plan       *roomParticipantPlan
	runtime    *roomParticipantRuntime
	err        error
	connected  bool
	connectErr error
}

func buildRoomParticipantPlans(opts RoomRunOptions, validation room.ValidationOptions) ([]*roomParticipantPlan, []string, error) {
	lookup := validation.LookupCredential
	if lookup == nil {
		lookup = os.LookupEnv
	}
	known := make(map[string]struct{}, len(opts.Manifest.Participants))
	secrets := make([]string, 0, len(opts.Manifest.Participants))
	for _, participant := range opts.Manifest.Participants {
		known[participant.ID] = struct{}{}
		if value, ok := lookup(participant.APIKeyEnv); ok && value != "" {
			secrets = append(secrets, value)
		}
	}
	for id := range opts.SessionInferencers {
		if _, ok := known[id]; !ok {
			return nil, secrets, fmt.Errorf("room session inferencer provided for unknown participant %q", id)
		}
	}
	toolFactory := opts.ToolCapabilitiesFactory
	if toolFactory == nil && roomManifestHasTools(opts.Manifest) {
		defaultFactory, factoryErr := newDefaultRoomParticipantToolCapabilitiesFactory(opts.ConfigDir)
		if factoryErr != nil {
			return nil, secrets, fmt.Errorf("%w: %v", ErrRoomParticipantToolsUnavailable, factoryErr)
		}
		toolFactory = defaultFactory
	}

	factory := opts.SessionFactory
	if factory == nil {
		factory = defaultRoomSessionFactory
	}
	plans := make([]*roomParticipantPlan, 0, len(opts.Manifest.Participants))
	for _, participant := range opts.Manifest.Participants {
		value, ok := lookup(participant.APIKeyEnv)
		if !ok {
			value = ""
		}
		if voice := strings.TrimSpace(participant.Voice); voice != "" {
			return nil, secrets, roomParticipantFailure(participant.ID, fmt.Errorf(
				"%w: participant %q requested voice %q, but SessionRunOptions.Voice is not available; omit voice or land the upstream voice contract first",
				ErrRoomParticipantVoiceUnavailable,
				participant.ID,
				voice,
			), []string{value})
		}
		sessionOptions := SessionRunOptions{
			Provider:        participant.Provider,
			Model:           participant.Model,
			ModelProvided:   true,
			APIKey:          value,
			BaseURL:         opts.BaseURL,
			ConfigDir:       opts.ConfigDir,
			WebSocketDialer: opts.WebSocketDialer,
			WaitForClose:    true,
		}
		if len(participant.Tools) > 0 {
			if toolFactory == nil {
				return nil, secrets, roomParticipantFailure(participant.ID, ErrRoomParticipantToolsUnavailable, []string{value})
			}
			capabilities, capabilityErr := toolFactory(participant)
			if capabilityErr != nil {
				return nil, secrets, roomParticipantFailure(participant.ID, fmt.Errorf("configure participant tools: %w", capabilityErr), []string{value})
			}
			if capabilityErr := validateRoomParticipantToolCapabilities(participant, capabilities); capabilityErr != nil {
				return nil, secrets, roomParticipantFailure(participant.ID, capabilityErr, []string{value})
			}
			sessionOptions.ToolExecutor = capabilities.Executor
			sessionOptions.ToolDefinitions = cloneRoomToolDefinitions(capabilities.Definitions)
		}
		if opts.WebSocketDialerFactory != nil {
			sessionOptions.WebSocketDialer = opts.WebSocketDialerFactory(participant)
		}
		plan := &roomParticipantPlan{manifest: participant, options: sessionOptions, secret: value}
		if inferencer, exists := opts.SessionInferencers[participant.ID]; exists {
			if nilInterface(inferencer) {
				return nil, secrets, roomParticipantFailure(participant.ID, errors.New("injected session inferencer is nil"), []string{value})
			}
			plan.inferencer = inferencer
		} else {
			inferencer, factoryErr := factory(participant, sessionOptions)
			if factoryErr != nil {
				return nil, secrets, roomParticipantFailure(participant.ID, fmt.Errorf("construct live session: %w", factoryErr), []string{value})
			}
			if nilInterface(inferencer) {
				return nil, secrets, roomParticipantFailure(participant.ID, errors.New("session factory returned a nil inferencer"), []string{value})
			}
			plan.inferencer = inferencer
		}
		plan.tracker = newRoomConnectTrackingInferencer(plan.inferencer)
		plans = append(plans, plan)
	}
	return plans, secrets, nil
}

func awaitRoomParticipantConnections(ctx context.Context, coordinator *roomCoordinator, plans []*roomParticipantPlan, timer *time.Timer, secrets []string) error {
	remaining := len(plans)
	for remaining > 0 {
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
		default:
		}
		progress := false
		for _, plan := range plans {
			if plan.participant != nil && plan.participant.plan != nil {
				// The tracker is created in the participant goroutine. It may not
				// exist during this first non-blocking scan.
				select {
				case err := <-plan.tracker.result:
					plan.participant.lifecycle.markConnected(err)
					remaining--
					progress = true
					if err != nil {
						return roomParticipantFailure(plan.manifest.ID, fmt.Errorf("connect live session: %w", err), append(secretsForPlan(plan), secrets...))
					}
				default:
				}
			}
		}
		if remaining == 0 {
			break
		}
		if !progress {
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
			case <-time.After(time.Millisecond):
			}
		}
	}
	for {
		allOpened := true
		for _, plan := range plans {
			if plan == nil || plan.participant == nil {
				continue
			}
			_, _, opened, closed, _, _, _ := plan.participant.lifecycle.snapshot()
			if opened || plan.participant.lifecycle.runHasFinished() {
				continue
			}
			if closed || plan.participant.lifecycle.transportHasEnded() {
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
		case <-time.After(time.Millisecond):
		}
	}
}

func finishRoomParticipant(coordinator *roomCoordinator, mesh *room.Mesh, result roomParticipantRunResult, secrets []string) {
	if result.runtime == nil || result.plan == nil {
		return
	}
	if result.connectErr != nil {
		result.runtime.lifecycle.markConnected(result.connectErr)
	}
	roomStopping := coordinator.isStopping()
	_, _, _, sessionClosed, closeReason, terminalReason, _ := result.runtime.lifecycle.snapshot()
	reason := classifyRoomParticipantTermination(roomStopping, result.err, result.connected, result.runtime.lifecycle.transportHasEnded(), sessionClosed, closeReason, terminalReason)
	coordinator.finishParticipant(result.runtime, reason, result.err, secrets, mesh)
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

func roomParticipantFailure(participantID string, err error, secrets []string) error {
	if err == nil {
		err = errors.New("unknown room participant failure")
	}
	return &roomSafeError{
		prefix:        fmt.Sprintf("room participant %q", participantID),
		participantID: participantID,
		cause:         err,
		secrets:       append([]string(nil), secrets...),
	}
}

func roomFailureResult(err error, secrets []string) RoomResult {
	return RoomResult{
		TerminationReason: RoomTerminationFailed,
		Reason:            RoomTerminationFailed,
		Error:             sanitizeRoomError(err, secrets),
		Participants:      make(map[string]RoomParticipantResult),
	}
}

type roomSafeError struct {
	prefix        string
	participantID string
	cause         error
	secrets       []string
}

func (e *roomSafeError) Error() string {
	if e == nil {
		return "room failure"
	}
	if e.cause == nil {
		return e.prefix
	}
	return e.prefix + ": " + sanitizeRoomError(e.cause, e.secrets)
}

func (e *roomSafeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func sanitizeRoomError(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return redactSelfPlayError(value, "")
}

func secretsForPlan(plan *roomParticipantPlan) []string {
	if plan == nil || plan.secret == "" {
		return nil
	}
	return []string{plan.secret}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func timerChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func sortedRoomIDs(ids []string) []string {
	result := append([]string(nil), ids...)
	for index := 1; index < len(result); index++ {
		value := result[index]
		position := index
		for position > 0 && result[position-1] > value {
			result[position] = result[position-1]
			position--
		}
		result[position] = value
	}
	return result
}

func sortRoomResultParticipants(participants map[string]RoomParticipantResult) {
	// Maps intentionally remain maps in the public result. This helper keeps a
	// single place for future ordered serialization without exposing secrets or
	// changing the runtime contract.
	_ = participants
}
