package services

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionProgressObserver_RejectedResultRegistersBeforeCallObservation(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "grok", "grok-realtime")
	observer.setToolResultsEnabled(true)
	const callID = "call-rejected-before-observed"
	outcome := messages.SessionSendOutcome{
		Status: messages.SessionSendBufferFull,
		Err:    errors.New("result queue is full"),
	}

	// The provider result can race ahead of the outer delta consumer. Rejection
	// must create the obligation so terminal diagnostics cannot lose the ID.
	observer.noteToolResultRejected(callID, outcome)
	observer.observeProviderToolCall(messages.NewToolCallEndValue(callID, "slow_tool", "{}"))

	if got := observer.unresolvedToolCallIDs(); len(got) != 1 || got[0] != callID {
		t.Fatalf("unresolved IDs after rejected-before-observed call = %v, want [%s]", got, callID)
	}
	statuses := observer.unresolvedToolResultSendStatuses()
	if statuses[callID] != messages.SessionSendBufferFull {
		t.Fatalf("rejected send status = %q, want %q", statuses[callID], messages.SessionSendBufferFull)
	}

	// A later duplicate rejection must not replace the first observable status,
	// and acceptance of this ID must not affect any other obligation.
	observer.noteToolResultRejected(callID, messages.SessionSendOutcome{Status: messages.SessionSendClosed})
	observer.noteToolResultRejected("call-other", messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure})
	observer.noteToolResultAccepted(callID)

	got := observer.unresolvedToolCallIDs()
	if len(got) != 1 || got[0] != "call-other" {
		t.Fatalf("unresolved IDs after accepting one result = %v, want [call-other]", got)
	}
	if statuses := observer.unresolvedToolResultSendStatuses(); statuses[callID] != "" {
		t.Fatalf("accepted call retained rejection status %q", statuses[callID])
	}
}
