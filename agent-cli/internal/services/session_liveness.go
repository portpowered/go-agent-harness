package services

import (
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// SessionSilentProviderEmptyResponseClassification identifies a provider
	// response that reached an explicit partial-output terminal boundary
	// without producing any admissible assistant output.
	SessionSilentProviderEmptyResponseClassification = "silent_provider_empty_response"
	// SessionSilentProviderTimeoutClassification is reserved for the
	// participant-owned watchdog failure implemented by the following liveness
	// story. Defining the taxonomy alongside the empty-response classification
	// keeps the result and diagnostic contracts closed over stable values.
	SessionSilentProviderTimeoutClassification = "silent_provider_timeout"
)

var (
	// ErrSilentProviderEmptyResponse is the stable sentinel for an explicit
	// empty provider response. The error intentionally contains no provider
	// status details or response payload.
	ErrSilentProviderEmptyResponse = errors.New("silent provider returned an empty response")
	// ErrSilentProviderTimeout is the stable sentinel for a provider that stops
	// emitting events while a response remains outstanding.
	ErrSilentProviderTimeout = errors.New("silent provider response timed out")
)

// SessionLivenessError carries the credential-free terminal facts associated
// with a provider liveness failure. Its Error string is deliberately stable so
// participant results can expose the condition without leaking provider data.
type SessionLivenessError struct {
	Classification     string
	ResponseID         string
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
	Usage              messages.TokenUsage
}

func (e *SessionLivenessError) Error() string {
	if e == nil {
		return "session liveness failure"
	}
	classification := strings.TrimSpace(e.Classification)
	if classification == "" {
		classification = SessionSilentProviderEmptyResponseClassification
	}
	return fmt.Sprintf("%s: provider response produced no observable output", classification)
}

func (e *SessionLivenessError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Classification {
	case SessionSilentProviderTimeoutClassification:
		return ErrSilentProviderTimeout
	default:
		return ErrSilentProviderEmptyResponse
	}
}

func newSilentProviderEmptyResponseError(msg messages.StreamMessage, value *messages.MessageEndValue) *SessionLivenessError {
	err := &SessionLivenessError{
		Classification:     SessionSilentProviderEmptyResponseClassification,
		ResponseID:         strings.TrimSpace(msg.ResponseID),
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
	if value != nil {
		err.Usage = value.Usage
	}
	return err
}

func sessionLivenessMetadata(err error) (classification string, terminalReason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) {
	var livenessErr *SessionLivenessError
	if !errors.As(err, &livenessErr) || livenessErr == nil {
		return "", "", "", ""
	}
	return livenessErr.Classification, livenessErr.TerminalReason, livenessErr.TerminalProvenance, livenessErr.OutputState
}

// responseHasToolLifecycleObligation snapshots the current response's tool
// state before MESSAGE.END processing clears toolCallInTurn. A pending result
// or continuation owns the terminal boundary and must not be diagnosed as an
// empty provider response.
func (o *sessionProgressObserver) responseHasToolLifecycleObligation() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	if o.toolCallInTurn || len(o.unresolvedToolCalls) > 0 {
		return true
	}
	for _, state := range o.toolContinuations {
		if state != nil && state.resultAccepted && !state.continuationComplete {
			return true
		}
	}
	return false
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

func (o *sessionProgressObserver) observeSilentProviderEmptyResponse(msg messages.StreamMessage, value *messages.MessageEndValue, outputPresent, toolObligation bool) {
	if o == nil || value == nil || outputPresent || toolObligation || responseCancellationBoundary(value) {
		return
	}
	if value.TerminalReason != messages.TerminalReasonPartialOutput ||
		value.OutputState != messages.TerminalOutputNone ||
		value.Usage.CompletionTokens != 0 {
		return
	}
	o.livenessMu.Lock()
	if o.livenessErr != nil || o.failure != nil {
		o.livenessMu.Unlock()
		return
	}
	err := newSilentProviderEmptyResponseError(msg, value)
	o.livenessErr = err
	o.failure = &failureFacts{
		classification: SessionSilentProviderEmptyResponseClassification,
		terminalReason: string(messages.TerminalReasonTerminalFailure),
		provenance:     string(messages.TerminalProvenanceSession),
		outputState:    string(messages.TerminalOutputNone),
		failingEvent:   string(messages.StreamTypeMessageEnd),
	}
	notify := o.livenessObserver
	o.livenessMu.Unlock()
	if notify != nil {
		notify(err)
	}
}

func (o *sessionProgressObserver) livenessFailure() error {
	if o == nil {
		return nil
	}
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	return o.livenessErr
}

func applyRoomParticipantTerminalMetadata(result *RoomParticipantResult, lifecycle *roomParticipantLifecycle, err error) {
	if result == nil {
		return
	}
	classification, terminalReason, provenance, outputState := "", messages.TerminalReason(""), messages.TerminalProvenance(""), messages.TerminalOutputState("")
	if lifecycle != nil {
		classification, terminalReason, provenance, outputState = lifecycle.terminalMetadata()
	}
	if classification == "" {
		classification, terminalReason, provenance, outputState = sessionLivenessMetadata(err)
	}
	if classification == "" {
		return
	}
	result.Classification = classification
	result.TerminalReason = terminalReason
	result.TerminalProvenance = provenance
	result.OutputState = outputState
}
