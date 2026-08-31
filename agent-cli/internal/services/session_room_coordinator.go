package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type roomCoordinator struct {
	done              chan struct{}
	admissionClosed   chan struct{}
	boundCancellation chan struct{}
	cancel            context.CancelFunc

	mu       sync.Mutex
	reason   RoomTerminationReason
	err      error
	active   map[string]*roomParticipantRuntime
	results  map[string]RoomParticipantResult
	maxTurns int
	progress chan struct{}
	// emptyStopBlocked keeps participant completion from terminating a replay
	// while its room-owned scheduler is still draining the final mixed frames.
	emptyStopBlocked      bool
	boundGrace            time.Duration
	bound                 bool
	boundForced           bool
	forceOnce             sync.Once
	doneOnce              sync.Once
	admissionOnce         sync.Once
	boundCancellationOnce sync.Once

	onParticipant       RoomParticipantObserver
	onParticipantFailed func(string, string)
	participantFailures map[string]struct{}
	onBoundShutdown     func(RoomTerminationReason)
}

func newRoomCoordinator(cancel context.CancelFunc, maxTurns int, boundGrace time.Duration, onParticipant RoomParticipantObserver, onBoundShutdown func(RoomTerminationReason)) *roomCoordinator {
	if boundGrace <= 0 {
		boundGrace = DefaultRoomBoundShutdownGrace
	}
	return &roomCoordinator{
		done:                make(chan struct{}),
		admissionClosed:     make(chan struct{}),
		boundCancellation:   make(chan struct{}),
		cancel:              cancel,
		active:              make(map[string]*roomParticipantRuntime),
		results:             make(map[string]RoomParticipantResult),
		maxTurns:            maxTurns,
		progress:            make(chan struct{}, 1),
		boundGrace:          boundGrace,
		onParticipant:       onParticipant,
		participantFailures: make(map[string]struct{}),
		onBoundShutdown:     onBoundShutdown,
	}
}

func (c *roomCoordinator) setParticipantFailureObserver(observer func(string, string)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.onParticipantFailed = observer
	c.mu.Unlock()
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
	if isRoomBoundTermination(reason) {
		c.beginBoundShutdown(reason, err)
		return
	}
	c.stopImmediately(reason, err)
}

func isRoomBoundTermination(reason RoomTerminationReason) bool {
	return reason == RoomTerminationMaxDurationReached || reason == RoomTerminationMaxTurnsReached
}

// beginBoundShutdown records the authoritative room bound and closes only the
// admission boundary. Existing participant responses retain their contexts
// until the fixed grace window expires.
func (c *roomCoordinator) beginBoundShutdown(reason RoomTerminationReason, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.reason != "" {
		c.mu.Unlock()
		return
	}
	c.reason = reason
	c.err = err
	c.bound = true
	runtimes := make([]*roomParticipantRuntime, 0, len(c.active))
	for _, runtime := range c.active {
		if runtime == nil {
			continue
		}
		runtimes = append(runtimes, runtime)
		if runtime.lifecycle != nil {
			runtime.lifecycle.markCoordinatorStopping(true, reason)
		}
	}
	c.mu.Unlock()

	c.closeAdmission()
	for _, runtime := range runtimes {
		if runtime.admissionCancel != nil {
			runtime.admissionCancel()
		}
	}
	if c.onBoundShutdown != nil {
		c.onBoundShutdown(reason)
	}
	go c.awaitBoundGrace()
}

func (c *roomCoordinator) awaitBoundGrace() {
	if c == nil {
		return
	}
	timer := time.NewTimer(c.boundGrace)
	defer timer.Stop()
	select {
	case <-timer.C:
		c.forceBoundShutdown()
	case <-c.done:
	}
}

// forceBoundShutdown is the deliberate second phase of a bound stop. It
// closes the session-loop cancellation signal before cancelling participant
// contexts, so the loop can drain any already-queued terminal deltas through
// its normal stop path.
func (c *roomCoordinator) forceBoundShutdown() {
	if c == nil {
		return
	}
	c.forceOnce.Do(func() {
		c.mu.Lock()
		if !c.bound {
			c.mu.Unlock()
			return
		}
		c.boundForced = true
		var firstFailure error
		runtimes := make([]*roomParticipantRuntime, 0, len(c.active))
		for _, runtime := range c.active {
			if runtime != nil {
				runtimes = append(runtimes, runtime)
				if runtime.lifecycle != nil {
					runtime.lifecycle.markBoundCancellation()
					observation := runtime.lifecycle.terminalObservationSnapshot()
					if firstFailure == nil && observation.failure {
						failureErr := observation.err
						if failureErr == nil {
							failureErr = errors.New("session stream error")
						}
						firstFailure = roomParticipantFailure(runtime.plan.manifest.ID, failureErr, secretsForPlan(runtime.plan))
					}
				}
			}
		}
		if firstFailure != nil {
			// A failure may have been accepted by the lifecycle immediately before
			// the force phase acquired the coordinator lock. Preserve that failure
			// rather than allowing the force phase to erase it as cancellation.
			c.reason = RoomTerminationFailed
			c.err = firstFailure
			c.bound = false
		}
		c.mu.Unlock()

		if firstFailure == nil {
			for _, runtime := range runtimes {
				if runtime != nil && runtime.lifecycle != nil {
					runtime.lifecycle.cancelActiveResponse()
				}
			}
		}

		c.boundCancellationOnce.Do(func() { close(c.boundCancellation) })
		c.doneOnce.Do(func() { close(c.done) })
		if c.cancel != nil {
			c.cancel()
		}
	})
}

func (c *roomCoordinator) stopImmediately(reason RoomTerminationReason, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.reason != "" {
		// A provider failure observed during the bound grace window is still
		// authoritative: the room has not deliberately cancelled the response
		// yet. Promote it to the room cause so the room result and all participant
		// projections agree. Once forceBoundShutdown has started, the bound owns
		// cancellation-only fallout and this path remains a no-op.
		if reason != RoomTerminationFailed || !c.bound || c.boundForced {
			c.mu.Unlock()
			return
		}
		c.reason = reason
		c.err = err
		c.bound = false
		for _, runtime := range c.active {
			if runtime != nil && runtime.lifecycle != nil {
				runtime.lifecycle.markCoordinatorStopping(false, reason)
			}
		}
		c.mu.Unlock()
		c.closeAdmission()
		c.doneOnce.Do(func() { close(c.done) })
		if c.cancel != nil {
			c.cancel()
		}
		return
	}
	c.reason = reason
	c.err = err
	for _, runtime := range c.active {
		if runtime != nil && runtime.lifecycle != nil {
			runtime.lifecycle.markCoordinatorStopping(false, reason)
		}
	}
	c.mu.Unlock()
	c.closeAdmission()
	c.doneOnce.Do(func() { close(c.done) })
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *roomCoordinator) closeAdmission() {
	if c == nil {
		return
	}
	c.admissionOnce.Do(func() { close(c.admissionClosed) })
}

func (c *roomCoordinator) admissionDone() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.admissionClosed
}

func (c *roomCoordinator) boundCancellationDone() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.boundCancellation
}

func (c *roomCoordinator) fail(err error) {
	if err == nil {
		err = errors.New("room failed")
	}
	c.stop(RoomTerminationFailed, err)
}

// failParticipant retires only the participant that owns err. Retirement is
// intentionally atomic with respect to activeExcept: once this method returns,
// later fan-out snapshots cannot include the failed participant. Cleanup and
// terminal notification remain in finishParticipant, which is driven by the
// participant's normal result path and therefore stays exactly-once.
func (c *roomCoordinator) failParticipant(participantID string, err error) {
	if c == nil || participantID == "" {
		return
	}
	if err == nil {
		err = errors.New("room participant failed")
	}
	runtime, empty, retired := c.retireParticipant(participantID, err)
	if !retired {
		return
	}
	c.notifyParticipantFailure(participantID, roomParticipantFailureReason(err, ParticipantTerminationError, "", false, nil))
	if runtime.cancel != nil {
		runtime.cancel()
	}
	if empty {
		// A participant fault never becomes the room verdict. Once no viable
		// participant remains, the room has completed normally at zero.
		c.stop(RoomTerminationStopped, nil)
	}
}

// notifyParticipantFailure publishes a participant failure at most once. The
// latch is separate from results because the event must be visible as soon as
// the active-set retirement is committed, before participant cleanup finishes.
func (c *roomCoordinator) notifyParticipantFailure(participantID, reason string) {
	if c == nil || participantID == "" {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "participant failure"
	}
	c.mu.Lock()
	if c.participantFailures == nil {
		c.participantFailures = make(map[string]struct{})
	}
	if _, alreadyNotified := c.participantFailures[participantID]; alreadyNotified {
		c.mu.Unlock()
		return
	}
	c.participantFailures[participantID] = struct{}{}
	observer := c.onParticipantFailed
	c.mu.Unlock()
	if observer != nil {
		observer(participantID, reason)
	}
}

// retireParticipant marks a live participant terminal and removes it from the
// active set under one coordinator lock. A room-level stop already in progress
// wins the race, because the resulting participant cancellation is teardown,
// not an independent participant fault.
func (c *roomCoordinator) retireParticipant(participantID string, err error) (*roomParticipantRuntime, bool, bool) {
	if c == nil || participantID == "" {
		return nil, false, false
	}
	c.mu.Lock()
	if c.reason != "" {
		c.mu.Unlock()
		return nil, false, false
	}
	runtime, ok := c.active[participantID]
	if !ok || runtime == nil {
		c.mu.Unlock()
		return nil, false, false
	}
	if runtime.lifecycle != nil {
		runtime.lifecycle.markParticipantFailure(err)
	}
	delete(c.active, participantID)
	empty := len(c.active) == 0 && !c.emptyStopBlocked
	c.mu.Unlock()
	return runtime, empty, true
}

// removeParticipantForFinish performs the non-faulting half of the normal
// participant terminal path. It also permits removal after a room-level stop,
// when the room has already claimed the shared done signal.
func (c *roomCoordinator) removeParticipantForFinish(participantID string) (bool, bool) {
	if c == nil || participantID == "" {
		return false, false
	}
	c.mu.Lock()
	_, ok := c.active[participantID]
	if ok {
		delete(c.active, participantID)
	}
	empty := ok && len(c.active) == 0 && !c.emptyStopBlocked
	c.mu.Unlock()
	return ok, empty
}

func (c *roomCoordinator) blockEmptyStop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.emptyStopBlocked = true
	c.mu.Unlock()
}

// unblockEmptyStop releases the setup barrier used while participant-local
// startup failures are being admitted. If setup leaves no viable participant,
// the room still completes through its clean empty-room taxonomy.
func (c *roomCoordinator) unblockEmptyStop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.emptyStopBlocked = false
	empty := len(c.active) == 0 && c.reason == ""
	c.mu.Unlock()
	if empty {
		c.stop(RoomTerminationStopped, nil)
	}
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

func (c *roomCoordinator) reasonSnapshot() RoomTerminationReason {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
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
		if roomParticipantIsHuman(participant.plan) {
			// A human has no provider response boundary to count. Room bounds
			// are satisfied by the provider-backed participants while local
			// capture/playback remains continuously available.
			continue
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
	roomStopping := c.isStopping()
	c.mu.Lock()
	if previous, alreadyFinished := c.results[id]; alreadyFinished {
		c.mu.Unlock()
		return previous
	}
	c.mu.Unlock()
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
	// Remove the participant before touching any of its resources. A fault may
	// already have retired it; normal completion removes it here. Either way,
	// activeExcept below sees only viable survivors.
	_, roomEmptyAfterRemoval := c.removeParticipantForFinish(id)
	if !roomStopping && (reason == ParticipantTerminationError || reason == ParticipantTerminationDisconnected) {
		c.notifyParticipantFailure(id, roomParticipantFailureReason(err, reason, closeReason, transportEnded, secrets))
	}

	// Remove the source from every surviving inbound mixer before closing its
	// own mixer. This discards only stale source bytes and keeps survivors live.
	for _, survivor := range c.activeExcept(id) {
		if survivor.mixer != nil {
			if removeErr := survivor.mixer.RemoveInput(id); removeErr != nil && !errors.Is(removeErr, room.ErrMixerInputMissing) && !errors.Is(removeErr, room.ErrMixerClosed) {
				// The surviving mixer owns this failure. Retire only that
				// participant; the source that is already leaving cannot turn a
				// sibling's mixer fault into a room verdict.
				survivorID := survivor.plan.manifest.ID
				c.failParticipant(survivorID, roomParticipantFailure(survivorID, removeErr, secrets))
			}
		}
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	var cleanupErr error
	if runtime.input != nil {
		cleanupErr = errors.Join(cleanupErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "input.device"), runtime.input.Close))
	}
	if runtime.output != nil {
		cleanupErr = errors.Join(cleanupErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "output.device"), runtime.output.Close))
	}
	if runtime.mixer != nil {
		cleanupErr = errors.Join(cleanupErr, boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "mixer"), runtime.mixer.Close))
	}
	if mesh != nil {
		if removeErr := boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "mesh"), func() error { return mesh.Remove(id) }); removeErr != nil && !errors.Is(removeErr, room.ErrMeshUnknownParticipant) && !errors.Is(removeErr, room.ErrMeshClosed) {
			cleanupErr = errors.Join(cleanupErr, removeErr)
		}
	}
	if cleanupErr != nil {
		// The participant has already been removed from active. Preserve its
		// cleanup failure in its own result instead of promoting it to room
		// failure or attempting a second terminal callback.
		err = errors.Join(err, roomParticipantFailure(id, cleanupErr, secrets))
	}
	if cleanupErr != nil && runtime.lifecycle != nil {
		runtime.lifecycle.markParticipantFailure(cleanupErr)
	}
	if terminalKind, terminalErr, terminalObserved := runtime.lifecycle.terminal(); terminalObserved {
		reason = terminalKind
		if terminalErr != nil {
			err = errors.Join(err, terminalErr)
		}
	}

	// observation carries the lifecycle's own honest projection of why this
	// participant stopped (a room-bound trigger and whether the in-flight
	// response completed inside grace or was cancelled after it). When the
	// lifecycle never latched a trigger/disposition, fall back to deriving
	// one from the same signals finishParticipant already computed above so
	// every participant result still carries a truthful terminal narrative.
	observation := runtime.lifecycle.terminalObservationSnapshot()
	terminationTrigger := observation.terminationTrigger
	if terminationTrigger == "" {
		switch {
		case observation.failure:
			terminationTrigger = ParticipantTerminationTriggerSessionFailure
		case isRoomBoundTermination(c.reasonSnapshot()):
			terminationTrigger = roomBoundTerminationTrigger(c.reasonSnapshot(), false)
		case c.isStopping():
			terminationTrigger = string(c.reasonSnapshot())
		case reason == ParticipantTerminationDisconnected:
			terminationTrigger = ParticipantTerminationTriggerProviderClose
		default:
			terminationTrigger = ParticipantTerminationTriggerParticipantCompletion
		}
	}
	terminationDisposition := observation.terminationDisposition
	if terminationDisposition == "" {
		switch {
		case observation.failure:
			terminationDisposition = ParticipantTerminationDispositionFailed
		case reason == ParticipantTerminationDisconnected:
			terminationDisposition = ParticipantTerminationDispositionDisconnected
		case c.isStopping() && !isRoomBoundTermination(c.reasonSnapshot()):
			terminationDisposition = ParticipantTerminationDispositionStopped
		default:
			terminationDisposition = ParticipantTerminationDispositionCompleted
		}
	}

	result := RoomParticipantResult{
		ID:                     id,
		ParticipantID:          id,
		TerminationReason:      reason,
		Reason:                 reason,
		TerminationTrigger:     terminationTrigger,
		TerminationDisposition: terminationDisposition,
		TurnsCompleted:         turns,
		Connected:              connected,
		Error:                  sanitizeRoomError(err, secrets),
	}
	applyRoomParticipantTerminalMetadata(&result, runtime.lifecycle, err)
	c.mu.Lock()
	if previous, alreadyFinished := c.results[id]; alreadyFinished {
		c.mu.Unlock()
		return previous
	}
	c.results[id] = result
	c.mu.Unlock()
	if c.onParticipant != nil {
		if observerErr := boundedRoomObserver(cleanup, roomLifecycleWorkLabel(id, "observer"), func() { c.onParticipant(result) }, runtime.markObserverDone); observerErr != nil {
			// Observer failures are owned by the participant notification
			// boundary. Do not stop a healthy sibling or invoke the observer a
			// second time.
			c.recordParticipantError(id, observerErr, secrets)
		}
	} else {
		runtime.markObserverDone()
	}
	if roomEmptyAfterRemoval && !c.isStopping() {
		c.stop(RoomTerminationStopped, nil)
	}
	return c.participantResult(id, result)
}

func (c *roomCoordinator) recordParticipantError(participantID string, err error, secrets []string) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	result, ok := c.results[participantID]
	if ok {
		var previousErr error
		if result.Error != "" {
			previousErr = errors.New(result.Error)
		}
		result.Error = sanitizeRoomError(errors.Join(previousErr, err), secrets)
		c.results[participantID] = result
	}
	c.mu.Unlock()
}

func (c *roomCoordinator) participantResult(participantID string, fallback RoomParticipantResult) RoomParticipantResult {
	if c == nil {
		return fallback
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if result, ok := c.results[participantID]; ok {
		return result
	}
	return fallback
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
