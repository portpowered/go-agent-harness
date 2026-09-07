package live

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func (h *handle) finish(err error) {
	h.finishOnce.Do(func() { h.finishOnceBody(err) })
}

func (h *handle) finishOnceBody(err error) {
	h.stopProviderLiveness()
	h.mu.Lock()
	if err == nil {
		err = h.startErr
	}
	userCancelled := h.userCancelled
	closeCapabilities, flushCapture := h.capabilityClose, h.captureFlush
	h.capabilityClose, h.captureFlush = nil, nil
	h.mu.Unlock()
	if closeCapabilities != nil {
		if closeErr := closeCapabilities(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close live capabilities: %w", closeErr))
		}
	}
	if flushCapture != nil {
		if flushErr := flushCapture(); flushErr != nil {
			err = errors.Join(err, fmt.Errorf("flush live capture: %w", flushErr))
		}
	}
	err = h.finishMedia(err, userCancelled)
	h.emitSynthesizedSessionClose()
	h.mu.Lock()
	terminalValue := cloneLiveTerminalValue(h.terminalValue)
	outputObserved := h.outputObserved
	h.mu.Unlock()
	if userCancelled {
		err = errors.Join(err, h.recorderError())
	} else {
		err = errors.Join(err, h.recorderError(), h.scheduledAudioError(), h.finiteAudioResponseError())
	}
	h.mu.Lock()
	h.terminalErr = err
	h.mu.Unlock()
	liveness := h.livenessFailureSnapshot()
	if liveness == nil {
		liveness = livenessFailureFromError(err)
	}
	if userCancelled && err == nil {
		terminalValue = userCancellationTerminalValue(h.request.SessionID, outputObserved)
	}
	terminalValue = finalizeLiveTerminalValue(h.request, err, terminalValue, liveness)
	h.publish(session.LiveEvent{Kind: string(session.LiveEventTerminal), SessionID: h.request.SessionID, Error: err, Liveness: liveness, Terminal: terminalValue, Critical: true}, true)
	close(h.done)
}

func (h *handle) markUserCancellation() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.userCancelled = true
	h.mu.Unlock()
}

func userCancellationTerminalValue(sessionID string, outputObserved bool) *messages.SessionCloseValue {
	outputState := messages.TerminalOutputNone
	if outputObserved {
		outputState = messages.TerminalOutputPartial
	}
	return messages.NewSessionCloseValueWithTerminal(
		sessionID,
		"user_cancelled",
		"user_cancelled",
		messages.TerminalReasonCancellation,
		messages.TerminalProvenanceCLI,
		outputState,
	)
}

func finalizeLiveTerminalValue(request session.LiveRequest, err error, value *messages.SessionCloseValue, liveness *session.LiveLivenessFailure) *messages.SessionCloseValue {
	if liveness != nil {
		return terminalForLiveness(request.SessionID, value, liveness)
	}
	if err == nil {
		return successfulLiveTerminal(request, value)
	}
	if errors.Is(err, session.ErrLiveDurationExceeded) {
		return messages.NewSessionCloseValueWithTerminal(
			request.SessionID,
			"max_duration",
			"max_duration",
			messages.TerminalReason("max_duration"),
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputPartial,
		)
	}
	if value != nil && value.TerminalReason != "" &&
		value.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion &&
		value.TerminalReason != messages.TerminalReasonReplayComplete {
		return value
	}
	if isContextTermination(err) {
		return messages.NewSessionCloseValueWithTerminal(
			request.SessionID,
			"",
			string(messages.TerminalReasonCancellation),
			messages.TerminalReasonCancellation,
			messages.TerminalProvenanceSession,
			messages.TerminalOutputNone,
		)
	}
	return messages.NewSessionCloseValueWithTerminal(
		request.SessionID,
		"",
		string(messages.TerminalReasonTerminalFailure),
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceSession,
		messages.TerminalOutputNone,
	)
}

func (h *handle) publish(event session.LiveEvent, terminal bool) {
	if h == nil {
		return
	}
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	if h.eventsClosed {
		return
	}
	if event.SessionID == "" {
		event.SessionID = h.request.SessionID
	}
	if event.ParticipantID == "" {
		event.ParticipantID = h.request.ParticipantID
	}
	if event.Timestamp.IsZero() && h.clock != nil {
		event.Timestamp = h.clock()
	}
	forced := terminal || event.Kind == string(session.LiveEventLiveness)
	if forced {
		h.reserveCriticalEventLocked()
	} else {
		reserve := 1
		if h.dropped > 0 {
			reserve = 2
		}
		if len(h.events) >= cap(h.events)-reserve {
			h.recordSequencedEventLocked(&event)
			h.dropped++
			return
		}
	}
	h.reportDroppedEventsLocked(event)
	h.recordSequencedEventLocked(&event)
	h.events <- event
	if terminal {
		close(h.events)
		h.eventsClosed = true
	}
}

func (h *handle) recordSequencedEventLocked(event *session.LiveEvent) {
	h.sequence++
	event.Sequence = h.sequence
	h.recordEvent(*event)
	if event.Kind == string(session.LiveEventTerminal) {
		h.includeTerminalRecordingError(event)
	}
}

func (h *handle) reportDroppedEventsLocked(event session.LiveEvent) {
	if h.dropped == 0 {
		return
	}
	overflow := session.LiveEvent{
		Kind:      string(session.LiveEventOverflow),
		SessionID: event.SessionID, ParticipantID: event.ParticipantID,
		Timestamp: event.Timestamp, Dropped: h.dropped, Critical: true,
	}
	h.dropped = 0
	h.recordSequencedEventLocked(&overflow)
	h.events <- overflow
}

func (h *handle) reserveCriticalEventLocked() {
	needed := 1
	if h.dropped > 0 || len(h.events) == cap(h.events) {
		needed = 2
	}
	for len(h.events) > cap(h.events)-needed {
		select {
		case <-h.events:
			h.dropped++
		default:
			return
		}
	}
}

func (h *handle) finishMedia(err error, userCancelled bool) error {
	if err == nil && !userCancelled {
		drainCtx, cancel := context.WithTimeout(h.evidenceContext(), defaultPlaybackDrainTimeout)
		if sealErr := h.media.SealInbound(); sealErr != nil {
			err = errors.Join(err, fmt.Errorf("seal live inbound media: %w", sealErr))
		}
		drainErr := h.media.DrainInbound(drainCtx)
		cancel()
		if drainErr != nil {
			err = errors.Join(err, fmt.Errorf("drain live inbound media: %w", drainErr))
		}
	}
	return errors.Join(err, h.media.Close())
}

func (h *handle) ensureCaptureTurnAdmissible() error {
	if h == nil {
		return session.ErrLiveClosed
	}
	h.mu.Lock()
	providerClosed := h.providerCloseObserved
	scheduled := h.scheduledAudioCount
	dispatched := h.dispatchedAudioCount
	completed := h.observedResponseTerminals - h.scheduledResponseBase
	terminal := cloneLiveTerminalValue(h.terminalValue)
	h.mu.Unlock()
	if !providerClosed {
		return nil
	}
	if incomplete := newScheduledAudioIncompleteError(scheduled, dispatched, completed, terminal); incomplete != nil {
		return incomplete
	}
	return session.ErrLiveClosed
}

func (h *handle) scheduledAudioError() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	scheduled := h.scheduledAudioCount
	dispatched := h.dispatchedAudioCount
	completed := h.observedResponseTerminals - h.scheduledResponseBase
	terminal := cloneLiveTerminalValue(h.terminalValue)
	h.mu.Unlock()
	return newScheduledAudioIncompleteError(scheduled, dispatched, completed, terminal)
}

func shouldDrainPlayback(ctx context.Context, waitErr error) bool {
	if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, session.ErrLiveDurationExceeded) {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return true
}

func isContextTermination(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type finishState struct {
	runErr          error
	providerErr     error
	pumpErr         error
	requested       bool
	requestedErr    error
	graceful        bool
	startErr        error
	continuationErr error
	toolResultErr   error
	parentDone      bool
	parentCause     error
}

func (h *handle) finishWhenStopped() {
	state := h.captureFinishState()
	if state.userCancellation() {
		h.markUserCancellation()
		h.finish(nil)
		return
	}
	h.finish(state.terminalError())
}

func (h *handle) captureFinishState() finishState {
	h.runWG.Wait()
	h.mu.Lock()
	state := finishState{
		runErr: h.runErr, providerErr: h.providerErr, pumpErr: h.pumpErr,
		requested: h.cancelRequested, requestedErr: h.cancelCause,
		graceful: h.gracefulStop, startErr: h.startErr,
	}
	parent := h.parentCtx
	h.mu.Unlock()
	h.toolMu.Lock()
	state.continuationErr = h.continuationErr
	h.toolMu.Unlock()
	state.toolResultErr = h.unresolvedToolResultsError()
	if state.continuationErr != nil && contextOnlyOrNil(state.requestedErr) {
		state.requested, state.requestedErr, state.graceful = true, state.continuationErr, false
	}
	if parent != nil && parent.Err() != nil {
		state.parentDone = true
		state.parentCause = context.Cause(parent)
		if state.parentCause == nil {
			state.parentCause = parent.Err()
		}
	}
	return state
}

func (s finishState) userCancellation() bool {
	if s.graceful || !errors.Is(s.parentCause, session.ErrLiveUserCancellation) || s.continuationErr != nil || s.toolResultErr != nil {
		return false
	}
	return contextOnlyOrNil(s.requestedErr) && contextOnlyOrNil(s.providerErr) &&
		contextOnlyOrNil(s.pumpErr) && contextOnlyOrNil(s.runErr) && contextOnlyOrNil(s.startErr)
}

func contextOnlyOrNil(err error) bool {
	return err == nil || isContextTermination(err)
}

func (s finishState) terminalError() error {
	if s.requested && !s.graceful {
		if s.toolResultErr != nil && contextOnlyOrNil(s.requestedErr) {
			return errors.Join(s.requestedErr, s.toolResultErr)
		}
		return s.requestedErr
	}
	if s.providerErr != nil && !isContextTermination(s.providerErr) {
		return fmt.Errorf("session error: %w", s.providerErr)
	}
	if s.toolResultErr != nil {
		return s.toolResultErr
	}
	if s.graceful {
		return nil
	}
	if s.parentDone {
		return s.parentCause
	}
	if s.pumpErr != nil {
		return s.pumpErr
	}
	if s.runErr != nil && !errors.Is(s.runErr, context.Canceled) {
		return fmt.Errorf("session error: %w", s.runErr)
	}
	return nil
}

func (h *handle) finishMessageObservation(msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeSessionClose && !h.deferProviderClose() {
		h.stopGracefully()
	}
}

func finalizeRecorder(recorder session.LiveRecorder, ctx context.Context, runErr error) error {
	if recorder == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("live recorder finalization context is required")
	}
	return recorder.Finalize(context.WithoutCancel(ctx), runErr)
}

func drainPlayback(parent context.Context, playback devices.Playback, timeout time.Duration) error {
	if playback == nil {
		return nil
	}
	if parent == nil {
		return errors.New("live playback drain context is required")
	}
	drainer, ok := playback.(interface{ WaitForPump(context.Context) error })
	if !ok {
		return nil
	}
	if timeout == 0 {
		timeout = defaultPlaybackDrainTimeout
	}
	if timeout < 0 {
		return errors.New("live playback drain timeout must not be negative")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := drainer.WaitForPump(ctx); err != nil {
		return fmt.Errorf("drain live playback: %w", err)
	}
	return nil
}
