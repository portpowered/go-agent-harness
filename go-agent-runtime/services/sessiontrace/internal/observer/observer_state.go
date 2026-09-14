package observer

import (
	"errors"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type SessionFinalAccounting = sessiontrace.SessionFinalAccounting
type SessionTokenUsageSemantics = sessiontrace.SessionTokenUsageSemantics

const SessionTokenUsageIncremental = sessiontrace.SessionTokenUsageIncremental

type failureFacts struct {
	classification string
	terminalReason string
	provenance     string
	outputState    string
	errorType      string
	code           string
	failingEvent   string
}

type sessionTerminalObservation = sessiontrace.TerminalObservation

func normalizeTerminalStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "completed" || status == "cancelled" || status == "failed" || status == "incomplete" {
		return status
	}
	return status
}

type platformSessionLivenessClock struct {
	source platformclock.TimerSource
}

func (c platformSessionLivenessClock) NewTimer(duration time.Duration) SessionLivenessTimer {
	return c.source.NewTimer(duration)
}

func sessionLivenessClockFromSource(source platformclock.Source) SessionLivenessClock {
	source = platformclock.Ensure(source)
	timerSource, ok := source.(platformclock.TimerSource)
	if !ok {
		return nil
	}
	return platformSessionLivenessClock{source: timerSource}
}

type SessionLivenessError = sessiontrace.LivenessError

func newSilentProviderEmptyResponseError(msg messages.StreamMessage, value *messages.MessageEndValue) *SessionLivenessError {
	err := &SessionLivenessError{Classification: SessionSilentProviderEmptyResponseClassification, ResponseID: strings.TrimSpace(msg.ResponseID), TerminalReason: messages.TerminalReasonTerminalFailure, TerminalProvenance: messages.TerminalProvenanceSession, OutputState: messages.TerminalOutputNone}
	if value != nil {
		err.Usage = value.Usage
	}
	return err
}

func sessionLivenessMetadata(err error) (string, messages.TerminalReason, messages.TerminalProvenance, messages.TerminalOutputState) {
	var livenessErr *SessionLivenessError
	if !errors.As(err, &livenessErr) || livenessErr == nil {
		return "", "", "", ""
	}
	return livenessErr.Classification, livenessErr.TerminalReason, livenessErr.TerminalProvenance, livenessErr.OutputState
}

func (o *observerState) responseHasToolLifecycleObligation() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	toolCallInTurn := o.toolCallInTurn
	unresolved := len(o.unresolvedToolCalls)
	o.toolStateMu.Unlock()
	if toolCallInTurn || unresolved > 0 {
		return true
	}
	return o.hasPendingToolContinuations()
}

func responseCancellationBoundary(value *messages.MessageEndValue) bool {
	if value == nil {
		return false
	}
	if isLocalResponseCancellation(value) || value.TerminalReason == messages.TerminalReasonCancellation {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(value.Status), "cancelled")
}

func (o *observerState) observeSilentProviderEmptyResponse(msg messages.StreamMessage, value *messages.MessageEndValue, outputPresent, toolObligation bool) {
	if o == nil || value == nil || outputPresent || toolObligation || responseCancellationBoundary(value) {
		return
	}
	if value.TerminalReason != messages.TerminalReasonPartialOutput || value.OutputState != messages.TerminalOutputNone || value.Usage.CompletionTokens != 0 {
		return
	}
	o.latchLivenessFailure(newSilentProviderEmptyResponseError(msg, value), &failureFacts{classification: SessionSilentProviderEmptyResponseClassification, terminalReason: string(messages.TerminalReasonTerminalFailure), provenance: string(messages.TerminalProvenanceSession), outputState: string(messages.TerminalOutputNone), failingEvent: string(messages.StreamTypeMessageEnd)})
}

func (o *observerState) setLivenessClock(clock SessionLivenessClock) {
	if o == nil || clock == nil {
		return
	}
	o.livenessMu.Lock()
	if !o.livenessStopped {
		o.livenessClock = clock
	}
	o.livenessMu.Unlock()
}

func (o *observerState) observeProviderEvent(msg messages.StreamMessage) {
	if o == nil || msg.Role == messages.RoleTool {
		return
	}
	if msg.Type == messages.StreamTypeMessageStart || msg.Type == messages.StreamTypeAudioStart {
		o.armProviderProgress()
		return
	}
	o.resetProviderProgress()
}

func (o *observerState) observeProviderDispatch(msg messages.StreamMessage) {
	if o == nil || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return
	}
	if msg.Type == messages.StreamTypeMessageEnd || msg.Type == messages.StreamTypeResponseCreate {
		o.armProviderProgress()
	}
}
