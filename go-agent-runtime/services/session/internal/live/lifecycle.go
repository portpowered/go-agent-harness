package live

import (
	"context"
	"errors"
	"fmt"

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
		err = errors.Join(h.finishMedia(err), h.recorderError())
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

func cloneLiveTerminalValue(value *messages.SessionCloseValue) *messages.SessionCloseValue {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func finalizeLiveTerminalValue(request session.LiveRequest, err error, value *messages.SessionCloseValue, liveness *session.LiveLivenessFailure) *messages.SessionCloseValue {
	if liveness != nil {
		return terminalForLiveness(request.SessionID, value, liveness)
	}
	if err == nil {
		return successfulLiveTerminal(request, value)
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

func terminalForLiveness(sessionID string, value *messages.SessionCloseValue, liveness *session.LiveLivenessFailure) *messages.SessionCloseValue {
	if value == nil {
		return messages.NewSessionCloseValueWithTerminal(
			sessionID,
			liveness.Classification,
			liveness.Classification,
			liveness.TerminalReason,
			liveness.TerminalProvenance,
			liveness.OutputState,
		)
	}
	copy := *value
	copy.Classification = liveness.Classification
	copy.TerminalReason = liveness.TerminalReason
	copy.TerminalProvenance = liveness.TerminalProvenance
	copy.OutputState = liveness.OutputState
	if copy.Reason == "" {
		copy.Reason = liveness.Classification
	}
	return &copy
}

func (h *handle) finishMedia(err error) error {
	if err == nil {
		// Normal completion drains the provider queue into the bounded host
		// port before closure. Failed or canceled sessions abort immediately.
		drainCtx, cancel := context.WithTimeout(h.evidenceContext(), defaultPlaybackDrainTimeout)
		drainErr := h.media.DrainInbound(drainCtx)
		cancel()
		if drainErr != nil {
			err = fmt.Errorf("drain live inbound media: %w", drainErr)
		}
	}
	return errors.Join(err, h.media.Close())
}

func isContextTermination(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func successfulLiveTerminal(request session.LiveRequest, value *messages.SessionCloseValue) *messages.SessionCloseValue {
	if value != nil && value.TerminalReason != "" {
		return value
	}
	if request.Replay.InputCapturePath != "" &&
		(request.ReplayPlan == nil || request.ReplayPlan.StopAfterResponse) {
		return messages.NewSessionCloseValueWithTerminal(
			request.SessionID,
			"",
			string(messages.TerminalReasonReplayComplete),
			messages.TerminalReasonReplayComplete,
			messages.TerminalProvenanceReplay,
			messages.TerminalOutputComplete,
		)
	}
	if value != nil && value.Reason != "" {
		return value
	}
	return messages.NewSessionCloseValueWithTerminal(
		request.SessionID,
		"",
		string(messages.TerminalReasonProviderAuthoredCompletion),
		messages.TerminalReasonProviderAuthoredCompletion,
		messages.TerminalProvenanceProvider,
		messages.TerminalOutputComplete,
	)
}

// A recorder can fail on the terminal observation itself. That failure must
// affect Wait and the delivered terminal even though the failed recorder
// cannot be asked recursively to record its own error.
func (h *handle) includeTerminalRecordingError(event *session.LiveEvent) {
	recordErr := h.recorderError()
	if recordErr == nil {
		return
	}
	if !errors.Is(event.Error, recordErr) {
		event.Error = errors.Join(event.Error, recordErr)
	}
	event.Terminal = finalizeLiveTerminalValue(h.request, event.Error, event.Terminal, event.Liveness)
	h.mu.Lock()
	h.terminalErr = event.Error
	h.mu.Unlock()
}
