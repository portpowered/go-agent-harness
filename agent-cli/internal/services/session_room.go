package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
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

	// Room lifecycle cleanup is deliberately finite. Provider/session contracts
	// are expected to make Close and cancellation progress, but a diagnostic is
	// preferable to making the room caller wait forever when an injected or
	// third-party owner violates that contract.
	roomCleanupTimeout   = time.Second
	roomAdmissionTimeout = 5 * time.Second

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
	// onParticipantSessionOpen is an internal deterministic lifecycle seam used
	// by package tests to release transport controls only after admission has
	// actually observed SESSION.OPEN.
	onParticipantSessionOpen func(string)
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
	plan            *roomParticipantPlan
	ctx             context.Context
	cancel          context.CancelFunc
	loopReady       chan *agentloop.AgentLoop
	participantDone chan struct{}
	mixerDone       chan struct{}
	observerDone    chan struct{}
	observerOnce    sync.Once
	mixer           *room.PCM16Mixer
	lifecycle       *roomParticipantLifecycle
}

func (r *roomParticipantRuntime) markObserverDone() {
	if r == nil || r.observerDone == nil {
		return
	}
	r.observerOnce.Do(func() { close(r.observerDone) })
}

type roomCleanupWaiter struct {
	timer *time.Timer
}

func (w *roomCleanupWaiter) start() {
	if w == nil || w.timer != nil {
		return
	}
	w.timer = time.NewTimer(roomCleanupTimeout)
}

func (w *roomCleanupWaiter) done() <-chan time.Time {
	if w == nil || w.timer == nil {
		return nil
	}
	return w.timer.C
}

func (w *roomCleanupWaiter) stop() {
	if w == nil || w.timer == nil {
		return
	}
	if !w.timer.Stop() {
		select {
		case <-w.timer.C:
		default:
		}
	}
	w.timer = nil
}

type roomLifecycleWorkError struct {
	outstanding []string
}

func (e *roomLifecycleWorkError) Error() string {
	if e == nil || len(e.outstanding) == 0 {
		return "room lifecycle work did not complete"
	}
	return "room lifecycle work did not complete: " + strings.Join(e.outstanding, "; ")
}

func newRoomLifecycleWorkError(outstanding ...string) error {
	seen := make(map[string]struct{}, len(outstanding))
	ordered := make([]string, 0, len(outstanding))
	for _, item := range outstanding {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	return &roomLifecycleWorkError{outstanding: ordered}
}

func roomLifecycleWorkLabel(participantID, phase string) string {
	if participantID == "" {
		return phase
	}
	return fmt.Sprintf("participant %q phase %s", participantID, phase)
}

func roomParticipantOutstandingWork(runtime *roomParticipantRuntime) []string {
	if runtime == nil || runtime.plan == nil {
		return []string{"participant runtime"}
	}
	id := runtime.plan.manifest.ID
	outstanding := make([]string, 0, 6)
	if runtime.plan.tracker == nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	} else if _, ready := runtime.plan.tracker.outcome(); !ready {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	}
	if runtime.lifecycle == nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "lifecycle"))
		return outstanding
	}
	created, closed, transportDone, closeErr := runtime.lifecycle.ownedSessionSnapshot()
	if created && !closed {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close"))
	}
	if closeErr != nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close.error"))
	}
	if created && !roomChannelClosed(transportDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.transport"))
	}
	if runtime.participantDone != nil && !roomChannelClosed(runtime.participantDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "participant.loop"))
	}
	if runtime.mixerDone != nil && !roomChannelClosed(runtime.mixerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "mixer"))
	}
	if runtime.observerDone != nil && !roomChannelClosed(runtime.observerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "observer"))
	}
	return outstanding
}

type roomParticipantLifecycle struct {
	mu                   sync.Mutex
	connected            bool
	connectErr           error
	sessionCreated       bool
	ownedSessionClosed   bool
	sessionCloseErr      error
	sessionOpened        bool
	sessionClosed        bool
	transportEnded       bool
	transportDone        <-chan struct{}
	stateChanged         chan<- struct{}
	roomStopping         bool
	runDone              bool
	closeReason          string
	terminalReason       messages.TerminalReason
	terminalKind         ParticipantTerminationReason
	terminalErr          error
	transportTerminalErr func() error
	turns                int
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

func (l *roomParticipantLifecycle) signalLocked() {
	if l == nil || l.stateChanged == nil {
		return
	}
	select {
	case l.stateChanged <- struct{}{}:
	default:
	}
}

func (l *roomParticipantLifecycle) markSessionCreated() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.sessionCreated = true
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) markOwnedSessionClosed(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.ownedSessionClosed = true
	l.sessionCloseErr = err
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) markTerminalLocked(reason ParticipantTerminationReason, err error) {
	if l.terminalKind != "" || reason == "" {
		return
	}
	l.terminalKind = reason
	l.terminalErr = err
}

func (l *roomParticipantLifecycle) markParticipantError(err error) {
	if l == nil || err == nil || roomCancellationOnly(err) {
		return
	}
	l.mu.Lock()
	if !l.roomStopping {
		l.markTerminalLocked(ParticipantTerminationError, err)
	}
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
		l.signalLocked()
	case messages.StreamTypeMessageEnd:
		l.turns++
	case messages.StreamTypeSessionClose:
		l.sessionClosed = true
		l.signalLocked()
		if value, ok := msg.Value.(*messages.SessionCloseValue); ok && value != nil {
			l.closeReason = value.Reason
			l.terminalReason = value.TerminalReason
			if l.terminalReason == "" && value.Reason == "provider_closed" {
				l.terminalReason = messages.TerminalReasonProviderClose
			}
		}
		if !l.roomStopping {
			terminalErr := error(nil)
			if l.transportTerminalErr != nil {
				terminalErr = l.transportTerminalErr()
			}
			if terminalErr != nil && !roomCancellationOnly(terminalErr) {
				l.markTerminalLocked(ParticipantTerminationError, terminalErr)
			} else {
				l.markTerminalLocked(classifyRoomSessionClose(l.closeReason, l.terminalReason), nil)
			}
		}
	}
	return l.turns
}

func (l *roomParticipantLifecycle) markTransportEndedWithError(terminalErr error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transportEnded = true
	l.signalLocked()
	if !l.roomStopping {
		if terminalErr != nil && !roomCancellationOnly(terminalErr) {
			l.markTerminalLocked(ParticipantTerminationError, terminalErr)
		} else {
			l.markTerminalLocked(ParticipantTerminationDisconnected, nil)
		}
	}
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) markTransportEnded() {
	l.markTransportEndedWithError(nil)
}

func (l *roomParticipantLifecycle) setTransportDone(done <-chan struct{}, terminalError func() error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transportDone = done
	l.transportTerminalErr = terminalError
	l.signalLocked()
	l.mu.Unlock()
	if roomChannelClosed(done) {
		var terminalErr error
		if terminalError != nil {
			terminalErr = terminalError()
		}
		l.markTransportEndedWithError(terminalErr)
	}
}

func (l *roomParticipantLifecycle) transportHasEnded() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	ended := l.transportEnded
	done := l.transportDone
	terminalError := l.transportTerminalErr
	l.mu.Unlock()
	if ended || done == nil {
		return ended
	}
	if roomChannelClosed(done) {
		var terminalErr error
		if terminalError != nil {
			terminalErr = terminalError()
		}
		l.markTransportEndedWithError(terminalErr)
		return true
	}
	return false
}

// markCoordinatorStopping records intentional room teardown before the
// coordinator cancels participant contexts. A transport that is already done
// at this boundary is causal; one that closes afterwards belongs to the
// coordinator's teardown and must not be reported as a provider disconnect.
func (l *roomParticipantLifecycle) markCoordinatorStopping() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if roomChannelClosed(l.transportDone) {
		l.transportEnded = true
		var terminalErr error
		if l.transportTerminalErr != nil {
			terminalErr = l.transportTerminalErr()
		}
		if terminalErr != nil && !roomCancellationOnly(terminalErr) {
			l.markTerminalLocked(ParticipantTerminationError, terminalErr)
		} else {
			l.markTerminalLocked(ParticipantTerminationDisconnected, nil)
		}
	}
	l.roomStopping = true
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) markRunDone(runErr error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.runDone = true
	l.signalLocked()
	if runErr != nil && !roomCancellationOnly(runErr) {
		l.markTerminalLocked(ParticipantTerminationError, runErr)
	}
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

func (l *roomParticipantLifecycle) snapshot() (connected bool, sessionOpened bool, sessionClosed bool, closeReason string, terminalReason messages.TerminalReason, turns int, connectErr error) {
	if l == nil {
		return false, false, false, "", "", 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connected, l.sessionOpened, l.sessionClosed, l.closeReason, l.terminalReason, l.turns, l.connectErr
}

func (l *roomParticipantLifecycle) terminal() (ParticipantTerminationReason, error, bool) {
	if l == nil {
		return "", nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.terminalKind, l.terminalErr, l.terminalKind != ""
}

func (l *roomParticipantLifecycle) ownedSessionSnapshot() (created, closed bool, transportDone <-chan struct{}, closeErr error) {
	if l == nil {
		return false, false, nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessionCreated, l.ownedSessionClosed, l.transportDone, l.sessionCloseErr
}

// roomConnectTrackingInferencer preserves the existing SessionInferencer
// contract while exposing the first ConnectSession outcome to the room's
// initial-start barrier.
type roomConnectTrackingInferencer struct {
	inner      messages.SessionInferencer
	result     chan error
	outcomes   chan<- roomConnectionOutcome
	once       sync.Once
	mu         sync.Mutex
	ready      bool
	connectErr error
	lifecycle  *roomParticipantLifecycle
}

type roomConnectionOutcome struct {
	tracker *roomConnectTrackingInferencer
	err     error
}

// roomTrackedSession makes the room's session owner explicit. The model
// runner remains responsible for calling Close, while this decorator records
// the completed ownership boundary and preserves the optional capabilities of
// the underlying provider session.
type roomTrackedSession struct {
	messages.Session
	lifecycle *roomParticipantLifecycle
	once      sync.Once
	closeErr  error
}

func (s *roomTrackedSession) Close() error {
	if s == nil || s.Session == nil {
		return nil
	}
	s.once.Do(func() {
		s.closeErr = s.Session.Close()
		if s.lifecycle != nil {
			s.lifecycle.markOwnedSessionClosed(s.closeErr)
		}
	})
	return s.closeErr
}

func (s *roomTrackedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if sender, ok := s.Session.(messages.SessionSendOutcomeSender); ok {
		return sender.SendWithOutcome(ctx, msg)
	}
	return messages.SendSessionWithOutcome(ctx, s.Session, msg)
}

func (s *roomTrackedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *roomTrackedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *roomTrackedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *roomTrackedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *roomTrackedSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *roomTrackedSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *roomTrackedSession) TerminalError() error {
	return terminalSessionError(s.Session)
}

func (s *roomTrackedSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.Session)
}

func newRoomConnectTrackingInferencer(inner messages.SessionInferencer) *roomConnectTrackingInferencer {
	return &roomConnectTrackingInferencer{inner: inner, result: make(chan error, 1)}
}

func (i *roomConnectTrackingInferencer) setOutcomeSink(outcomes chan<- roomConnectionOutcome) {
	if i == nil {
		return
	}
	i.outcomes = outcomes
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
		if i.outcomes != nil {
			i.outcomes <- roomConnectionOutcome{tracker: i, err: err}
		}
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
	if err != nil {
		if session != nil {
			if i.lifecycle != nil {
				i.lifecycle.markSessionCreated()
			}
			tracked := &roomTrackedSession{Session: session, lifecycle: i.lifecycle}
			if i.lifecycle != nil {
				terminalError := func() error { return terminalSessionError(tracked) }
				i.lifecycle.setTransportDone(tracked.Done(), terminalError)
			}
			if closeErr := tracked.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close failed session: %w", closeErr))
			}
		}
		i.publish(err)
		return nil, err
	}
	if err == nil && session != nil && i.lifecycle != nil {
		i.lifecycle.markSessionCreated()
		tracked := &roomTrackedSession{Session: session, lifecycle: i.lifecycle}
		terminalError := func() error { return terminalSessionError(tracked) }
		i.lifecycle.setTransportDone(tracked.Done(), terminalError)
		recordSessionEnd := func() {
			i.lifecycle.markTransportEndedWithError(terminalError())
		}
		go func() {
			select {
			case <-tracked.Done():
				recordSessionEnd()
			case <-ctx.Done():
				if roomChannelClosed(tracked.Done()) {
					recordSessionEnd()
				}
			}
		}()
		session = tracked
	}
	i.publish(err)
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
	progress chan struct{}

	onParticipant RoomParticipantObserver
}

func newRoomCoordinator(cancel context.CancelFunc, maxTurns int, onParticipant RoomParticipantObserver) *roomCoordinator {
	return &roomCoordinator{
		done:          make(chan struct{}),
		cancel:        cancel,
		active:        make(map[string]*roomParticipantRuntime),
		results:       make(map[string]RoomParticipantResult),
		maxTurns:      maxTurns,
		progress:      make(chan struct{}, 1),
		onParticipant: onParticipant,
	}
}

func (c *roomCoordinator) recordError(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	c.err = errors.Join(c.err, err)
	c.mu.Unlock()
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
	for _, runtime := range c.active {
		if runtime != nil && runtime.lifecycle != nil {
			runtime.lifecycle.markCoordinatorStopping()
		}
	}
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

func (c *roomCoordinator) participantRunError(participantID string, err error) error {
	if err == nil || c == nil || !c.isStopping() {
		return err
	}
	roomErr := c.roomError()
	failedID := c.failedParticipantID()
	if failedID != "" && failedID != participantID {
		if roomErr != nil && errors.Is(err, roomErr) {
			return nil
		}
		if roomCancellationOnly(err) {
			return nil
		}
	}
	if roomErr == nil && roomCancellationOnly(err) {
		return nil
	}
	return err
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
		_, _, _, _, _, completed, _ := participant.lifecycle.snapshot()
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

func (c *roomCoordinator) finishParticipant(runtime *roomParticipantRuntime, reason ParticipantTerminationReason, err error, secrets []string, mesh *room.Mesh, cleanup *roomCleanupWaiter) RoomParticipantResult {
	if runtime == nil || runtime.plan == nil {
		return RoomParticipantResult{Reason: ParticipantTerminationError, Error: "room participant runtime is nil"}
	}
	id := runtime.plan.manifest.ID
	connected, _, sessionClosed, closeReason, terminalReason, turns, connectErr := runtime.lifecycle.snapshot()
	transportEnded := runtime.lifecycle.transportHasEnded()
	_, _, _, sessionCloseErr := runtime.lifecycle.ownedSessionSnapshot()
	if connectErr != nil && err == nil {
		err = connectErr
	}
	if sessionCloseErr != nil {
		err = errors.Join(err, fmt.Errorf("close participant session: %w", sessionCloseErr))
	}
	// A room-level failure is returned by every session loop through DoneErr.
	// Keep it on the participant that caused the failure, but do not turn the
	// coordinator's cancellation into an error for surviving participants.
	err = c.participantRunError(id, err)
	if terminalKind, terminalErr, terminalObserved := runtime.lifecycle.terminal(); terminalObserved {
		// Lifecycle observations are authoritative once latched. In particular,
		// coordinator cancellation must not replace a transport or typed session
		// close that was observed first.
		reason = terminalKind
		if terminalErr != nil {
			err = terminalErr
		} else if terminalKind != ParticipantTerminationError && sessionCloseErr == nil {
			err = nil
		}
	} else if reason == "" {
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
	var cleanupErr error
	if runtime.mixer != nil {
		cleanupErr = errors.Join(cleanupErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "mixer"), runtime.mixer.Close))
	}
	if mesh != nil {
		if removeErr := boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "mesh"), func() error { return mesh.Remove(id) }); removeErr != nil && !errors.Is(removeErr, room.ErrMeshUnknownParticipant) && !errors.Is(removeErr, room.ErrMeshClosed) {
			cleanupErr = errors.Join(cleanupErr, removeErr)
		}
	}
	if cleanupErr != nil {
		if c.isStopping() {
			c.recordError(cleanupErr)
		} else {
			c.fail(roomParticipantFailure(id, cleanupErr, secrets))
		}
	}
	if c.onParticipant != nil {
		if observerErr := boundedRoomObserver(cleanup, roomLifecycleWorkLabel(id, "observer"), func() { c.onParticipant(result) }, runtime.markObserverDone); observerErr != nil {
			if c.isStopping() {
				c.recordError(observerErr)
			} else {
				c.fail(roomParticipantFailure(id, observerErr, secrets))
			}
		}
	} else {
		runtime.markObserverDone()
	}
	if shouldFailEmpty {
		c.fail(fmt.Errorf("all room participants terminated"))
	}
	return result
}

func (c *roomCoordinator) snapshot() (RoomTerminationReason, map[string]RoomParticipantResult, []string, error) {
	if c == nil {
		return RoomTerminationFailed, nil, nil, errors.New("room coordinator is nil")
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
	return c.reason, results, active, c.err
}

func classifyRoomParticipantTermination(roomStopping bool, runErr error, connected bool, transportEnded bool, sessionClosed bool, closeReason string, terminalReason messages.TerminalReason) ParticipantTerminationReason {
	if runErr != nil && !roomCancellationOnly(runErr) {
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

func roomCancellationOnly(err error) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !roomCancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return roomCancellationOnly(cause)
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func classifyRoomSessionClose(closeReason string, terminalReason messages.TerminalReason) ParticipantTerminationReason {
	if closeReason == "provider_closed" || terminalReason == messages.TerminalReasonProviderClose {
		return ParticipantTerminationDisconnected
	}
	if terminalReason == messages.TerminalReasonTerminalFailure {
		return ParticipantTerminationError
	}
	return ParticipantTerminationEnded
}

func roomChannelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
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
			safeErr = errors.Join(safeErr, closeMeshNow())
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
		return cleanupRoomParticipantSetup(runtimes, mesh, cleanup)
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
			_, opened, closed, _, _, _, _ := plan.participant.lifecycle.snapshot()
			if opened {
				continue
			}
			if closed || plan.participant.lifecycle.transportHasEnded() || plan.participant.lifecycle.runHasFinished() {
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
