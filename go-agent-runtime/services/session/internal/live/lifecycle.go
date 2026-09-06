package live

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func (h *handle) finishWhenStopped() {
	h.runWG.Wait()
	h.mu.Lock()
	runErr := h.runErr
	providerErr := h.providerErr
	pumpErr := h.pumpErr
	requested := h.cancelRequested
	requestedErr := h.cancelCause
	graceful := h.gracefulStop
	parent := h.parentCtx
	h.mu.Unlock()
	h.toolMu.Lock()
	continuationErr := h.continuationErr
	h.toolMu.Unlock()
	if continuationErr != nil && (requestedErr == nil || isContextTermination(requestedErr)) {
		requested = true
		requestedErr = continuationErr
		graceful = false
	}

	var terminal error
	if requested && !graceful {
		terminal = requestedErr
	} else if providerErr != nil && !isContextTermination(providerErr) {
		// A provider transport can publish its final close boundary before its
		// Done watcher observes the transport error. That boundary asks the
		// runtime to stop gracefully, but it must not erase an actionable
		// replay mismatch or provider write/read failure.
		terminal = providerErr
	} else if graceful {
		terminal = nil
	} else if parent != nil && parent.Err() != nil {
		terminal = context.Cause(parent)
		if terminal == nil {
			terminal = parent.Err()
		}
	} else if pumpErr != nil {
		terminal = pumpErr
	} else if runErr != nil && !errors.Is(runErr, context.Canceled) {
		terminal = runErr
	}
	h.finish(terminal)
}

func (h *handle) finish(err error) {
	h.finishOnce.Do(func() {
		h.stopProviderLiveness()
		h.mu.Lock()
		if err == nil {
			err = h.startErr
		}
		terminalValue := cloneLiveTerminalValue(h.terminalValue)
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
		err = errors.Join(h.finishMedia(err), h.recorderError(), h.scheduledAudioError())
		h.mu.Lock()
		h.terminalErr = err
		h.mu.Unlock()
		liveness := h.livenessFailureSnapshot()
		if liveness == nil {
			liveness = livenessFailureFromError(err)
		}
		terminalValue = finalizeLiveTerminalValue(h.request, err, terminalValue, liveness)
		h.publish(session.LiveEvent{Kind: string(session.LiveEventTerminal), SessionID: h.request.SessionID, Error: err, Liveness: liveness, Terminal: terminalValue, Critical: true}, true)
		close(h.done)
	})
}

func finalizeLiveTerminalValue(request session.LiveRequest, err error, value *messages.SessionCloseValue, liveness *session.LiveLivenessFailure) *messages.SessionCloseValue {
	if liveness != nil {
		return terminalForLiveness(request.SessionID, value, liveness)
	}
	if err == nil {
		return successfulLiveTerminal(request, value)
	}
	if errors.Is(err, session.ErrLiveDurationExceeded) {
		// MaxDuration is a deliberate loop boundary. Preserve the partial
		// response that was already published while making the terminal reason
		// explicit, even when the provider had emitted a close value first.
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
		// Reserve terminal delivery, plus any pending overflow report.
		reserve := 1
		if h.dropped > 0 {
			reserve = 2
		}
		if len(h.events) >= cap(h.events)-reserve {
			// Evidence retains observations dropped from the presentation
			// stream, with their original position in the same sequence.
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

// Reserve the critical observation and any overflow report before recording
// either. Evictions are then included in that report instead of being lost
// when the final observation closes the channel.
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

func (h *handle) finishMedia(err error) error {
	if err == nil {
		// Normal completion drains the provider queue into the bounded host
		// port before closure. Failed or canceled sessions abort immediately.
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

// observeResponseTerminal keeps a response count independent of
// FinishAfterResponse. Scheduled finite feeds use this count to report a
// truthful outcome even when a provider closes a persistent session before
// the normal finite-response policy is enabled.
func (h *handle) observeResponseTerminal(msg messages.StreamMessage) {
	if h == nil || msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool ||
		(msg.Role != "" && msg.Role != messages.RoleAssistant) {
		return
	}
	h.mu.Lock()
	// Ordinary live sessions use their normal finite-response bookkeeping and
	// do not need a second response identity ledger. Keeping this accounting
	// behind the scheduled-audio admission also bounds the ledger by the
	// caller's finite schedule rather than by the lifetime of a conversation.
	if h.scheduledAudioCount <= 0 {
		h.mu.Unlock()
		return
	}
	completed := h.observedResponseTerminals - h.scheduledResponseBase
	if completed >= h.scheduledAudioCount {
		h.mu.Unlock()
		return
	}
	if responseID := strings.TrimSpace(msg.ResponseID); responseID != "" {
		if h.observedResponseIDs == nil {
			h.observedResponseIDs = make(map[string]struct{})
		}
		if _, seen := h.observedResponseIDs[responseID]; seen {
			h.mu.Unlock()
			return
		}
		h.observedResponseIDs[responseID] = struct{}{}
	}
	h.observedResponseTerminals++
	if h.observedResponseTerminals-h.scheduledResponseBase >= h.scheduledAudioCount {
		// The schedule has a complete set of terminal dispositions. Release the
		// identity set so a long lived handle cannot retain it after completion.
		h.observedResponseIDs = nil
	}
	h.mu.Unlock()
}

func (h *handle) configureScheduledAudio(scheduled, responseBase int) {
	if h == nil || scheduled <= 0 {
		return
	}
	h.mu.Lock()
	h.scheduledAudioCount = scheduled
	if responseBase > 0 {
		h.scheduledResponseBase = responseBase
	}
	h.mu.Unlock()
}

func (h *handle) noteCaptureDispatched() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.dispatchedAudioCount < h.scheduledAudioCount {
		h.dispatchedAudioCount++
	}
	h.mu.Unlock()
}

// ensureCaptureTurnAdmissible checks the provider boundary before opening a
// new caller-owned finite source. A provider close remains authoritative even
// if its already queued response terminal is delivered afterward; admitting
// another source would report a false dispatch and could write after close.
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

func newScheduledAudioIncompleteError(scheduled, dispatched, completed int, terminal *messages.SessionCloseValue) error {
	if scheduled <= 0 {
		return nil
	}
	if completed < 0 {
		completed = 0
	}
	if completed > scheduled {
		completed = scheduled
	}
	if dispatched > scheduled {
		dispatched = scheduled
	}
	if completed >= scheduled && dispatched >= scheduled {
		return nil
	}
	incomplete := &session.LiveScheduledAudioIncompleteError{
		Completed:  completed,
		Dispatched: dispatched,
		Scheduled:  scheduled,
	}
	if terminal != nil {
		incomplete.ProviderStatus = terminal.Classification
		incomplete.ProviderDetails = terminal.Reason
	}
	return incomplete
}

// deferProviderClose lets a finite scheduled feed drain provider response
// terminals that were already queued before SESSION.CLOSE. Ordinary live
// sessions retain the eager close behavior; the provider Done boundary still
// ends the loop when an undispatched scheduled source remains.
func (h *handle) deferProviderClose() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.scheduledAudioCount > 0 && h.dispatchedAudioCount < h.scheduledAudioCount
}
