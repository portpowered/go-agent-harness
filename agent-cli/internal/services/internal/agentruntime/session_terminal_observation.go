package agentruntime

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// sessionTerminalObservation is the bounded, provider-neutral bridge between
// session diagnostics and the owner of a session lifecycle. It carries error
// identity only in-process; public result and evidence boundaries sanitize it.
type sessionTerminalObservation struct {
	ResponseID         string
	Classification     string
	TerminalReason     string
	TerminalProvenance string
	OutputState        string
	Err                error
	Failure            bool
	RoomBound          bool
	Code               string
	FailingEvent       string
}

const RoomBoundCancelledClassification = providers.ErrorClassRoomBoundCancelled

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

func sessionTerminalObservationFromFailure(facts *failureFacts, err error) sessionTerminalObservation {
	if facts == nil {
		return sessionTerminalObservation{}
	}
	if err == nil && facts.errorType != "" {
		err = errors.New(facts.errorType)
	}
	if err == nil {
		err = errors.New("session stream error")
	}
	return sessionTerminalObservation{
		Classification:     facts.classification,
		TerminalReason:     facts.terminalReason,
		TerminalProvenance: facts.provenance,
		OutputState:        facts.outputState,
		Err:                err,
		Failure:            true,
		Code:               facts.code,
		FailingEvent:       facts.failingEvent,
	}
}

func sessionTerminalObservationFromMessageEnd(responseID string, value *messages.MessageEndValue) sessionTerminalObservation {
	if value == nil {
		return sessionTerminalObservation{}
	}
	reason := value.TerminalReason
	if reason == "" {
		reason = messages.TerminalReasonProviderAuthoredCompletion
	}
	provenance := value.TerminalProvenance
	if provenance == "" {
		provenance = messages.TerminalProvenanceProvider
	}
	outputState := value.OutputState
	if outputState == "" {
		outputState = messages.TerminalOutputComplete
	}
	return sessionTerminalObservation{
		ResponseID:         responseID,
		TerminalReason:     string(reason),
		TerminalProvenance: string(provenance),
		OutputState:        string(outputState),
	}
}

func sessionTerminalObservationForCancellation(outputState messages.TerminalOutputState, roomBound bool) sessionTerminalObservation {
	if outputState == "" {
		outputState = messages.TerminalOutputNone
	}
	classification := providers.ErrorClassCancellation
	provenance := messages.TerminalProvenanceCLI
	if roomBound {
		classification = RoomBoundCancelledClassification
		provenance = messages.TerminalProvenanceRoom
	}
	return sessionTerminalObservation{
		Classification:     classification,
		TerminalReason:     string(messages.TerminalReasonCancellation),
		TerminalProvenance: string(provenance),
		OutputState:        string(outputState),
		RoomBound:          roomBound,
	}
}

func (o *sessionProgressObserver) notifyFinalTerminalObservation(err error) {
	if o == nil || o.terminalObserver == nil {
		return
	}
	if o.failure != nil {
		o.notifyFailureObservation(sessionTerminalObservationFromFailure(o.failure, err))
		return
	}
	if o.roomBoundCancellation && o.failure == nil && roomCancellationOnly(err) {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), true))
		return
	}
	if o.userCancelled {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), false))
		return
	}
	if err != nil && !roomCancellationOnly(err) {
		facts := factsFromSessionRunError(err)
		if facts == nil {
			classification := providers.ErrorClassification(err)
			if classification == "" {
				classification = providers.ErrorClassUnknown
			}
			facts = &failureFacts{
				classification: classification,
				terminalReason: string(messages.TerminalReasonTerminalFailure),
				provenance:     string(messages.TerminalProvenanceCLI),
				outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
				failingEvent:   failingEventRun,
			}
		}
		o.acceptFailureObservation(facts, err)
		return
	}
	if roomCancellationOnly(err) {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), false))
	}
}
