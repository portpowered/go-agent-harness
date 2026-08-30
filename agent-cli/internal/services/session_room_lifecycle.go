package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
)

type roomParticipantPlan struct {
	manifest              room.Participant
	options               SessionRunOptions
	inferencer            messages.SessionInferencer
	secret                string
	tracker               *roomConnectTrackingInferencer
	participant           *roomParticipantRuntime
	capabilityCoordinator *SessionCapabilityCoordinator
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

// observeAdmittedTurn advances room progress only after the shared session
// observer has accepted a provider response as a completed turn. Raw
// MESSAGE.END events are intentionally not sufficient: a provider can emit an
// empty response boundary before producing any assistant output.
func (l *roomParticipantLifecycle) observeAdmittedTurn() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	l.turns++
	l.signalLocked()
	turns := l.turns
	l.mu.Unlock()
	return turns
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
