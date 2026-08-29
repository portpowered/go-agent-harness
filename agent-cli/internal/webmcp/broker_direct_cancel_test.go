package webmcp

import (
	"context"
	"encoding/json"
	"testing"
)

type mismatchedDirectCancelSession struct {
	page        PageContext
	cancelCount int
}

func (s *mismatchedDirectCancelSession) Context() PageContext { return s.page }

func (s *mismatchedDirectCancelSession) Ownership() TargetOwnership {
	return TargetOwnershipExternal
}

func (s *mismatchedDirectCancelSession) EnableWebMCP(context.Context) error { return nil }

func (s *mismatchedDirectCancelSession) Events() <-chan BrowserEvent {
	return make(chan BrowserEvent)
}

func (s *mismatchedDirectCancelSession) InvokeWebMCP(context.Context, FrameID, string, json.RawMessage) (InvocationID, error) {
	return "", nil
}

func (s *mismatchedDirectCancelSession) CancelWebMCP(context.Context, InvocationID) error {
	s.cancelCount++
	return nil
}

func (s *mismatchedDirectCancelSession) Done() <-chan struct{} { return make(chan struct{}) }

func (s *mismatchedDirectCancelSession) Err() error { return nil }

func (s *mismatchedDirectCancelSession) Close() error { return nil }

func TestDirectCancelRejectsSessionIdentityMismatchBeforeDispatch(t *testing.T) {
	const (
		browserID = BrowserID("browser-exact")
		targetID  = TargetID("target-exact")
	)
	session := &mismatchedDirectCancelSession{page: PageContext{
		Key:       PageKey{BrowserID: browserID, TargetID: "different-target"},
		Connected: true,
	}}
	broker := NewBroker(BrokerOptions{})
	broker.selected = &brokerSession{
		session: session,
		context: PageContext{Key: PageKey{BrowserID: browserID, TargetID: targetID}, Connected: true, Generation: 1},
		active:  true,
	}

	err := broker.CancelDirect(context.Background(), DirectCancelRequest{
		Target:       TargetSelector{BrowserID: browserID, TargetID: targetID},
		InvocationID: "browser-receipt-exact",
	})
	classified, ok := err.(*ClassifiedError)
	if !ok || classified.Code != ErrorStaleSelection || classified.Details["reason"] != "exact_target_session_mismatch" {
		t.Fatalf("direct cancel error = %#v, want exact target-session stale selection", err)
	}
	if session.cancelCount != 0 {
		t.Fatalf("cancel dispatch count = %d, want zero on session mismatch", session.cancelCount)
	}
}
