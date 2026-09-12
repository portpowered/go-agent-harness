package sessioncontinuation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
)

func TestUnresolvedToolResultsErrorOwnsSnapshots(t *testing.T) {
	err := &sessioncontinuation.UnresolvedToolResultsError{
		CallIDs:      []string{"call-a", "call-b"},
		SendStatuses: map[string]messages.SessionSendStatus{"call-a": messages.SessionSendClosed},
	}
	if got := err.Error(); !strings.Contains(got, "call-a, call-b") || !strings.Contains(got, "call-a=closed") {
		t.Fatalf("unresolved error = %q, want IDs and send status", got)
	}
	if !errors.Is(err, sessioncontinuation.ErrUnresolvedToolResults) {
		t.Fatalf("unresolved error lost sentinel identity: %v", err)
	}
	ids := err.UnresolvedCallIDs()
	statuses := err.SendStatusSnapshot()
	ids[0] = "mutated"
	statuses["call-a"] = messages.SessionSendTimedOut
	if err.CallIDs[0] != "call-a" || err.SendStatuses["call-a"] != messages.SessionSendClosed {
		t.Fatalf("error snapshot aliases were exposed: ids=%v statuses=%v", ids, statuses)
	}
	var nilErr *sessioncontinuation.UnresolvedToolResultsError
	if nilErr.Error() != sessioncontinuation.ErrUnresolvedToolResults.Error() || nilErr.UnresolvedCallIDs() != nil || nilErr.SendStatusSnapshot() != nil {
		t.Fatal("nil unresolved error did not return safe empty snapshots")
	}
}
