package webmcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"
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
	classified, ok := errors.AsType[*ClassifiedError](err)
	if !ok || classified.Code != ErrorStaleSelection || classified.Details["reason"] != "exact_target_session_mismatch" {
		t.Fatalf("direct cancel error = %#v, want exact target-session stale selection", err)
	}
	if session.cancelCount != 0 {
		t.Fatalf("cancel dispatch count = %d, want zero on session mismatch", session.cancelCount)
	}
}

// TestCallerBoundContextMirrorsCallerDeadlineAndCancellation proves the queued
// dispatch context reports an expired caller deadline as DeadlineExceeded (so
// target diagnostics classify a timeout) and a caller cancellation as Canceled
// carrying the caller's exact cancellation cause.
func TestCallerBoundContextMirrorsCallerDeadlineAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		caller, cancelCaller := context.WithTimeout(t.Context(), time.Second)
		defer cancelCaller()
		dispatch, release := callerBoundContext(t.Context(), newCallerBinding(caller))
		defer release()
		<-dispatch.Done()
		if !errors.Is(dispatch.Err(), context.DeadlineExceeded) {
			t.Fatalf("dispatch after caller deadline = %v, want %v", dispatch.Err(), context.DeadlineExceeded)
		}
	})

	callerStopped := errors.New("caller stopped the invocation")
	caller, cancelCaller := context.WithCancelCause(t.Context())
	dispatch, release := callerBoundContext(t.Context(), newCallerBinding(caller))
	defer release()
	cancelCaller(callerStopped)
	<-dispatch.Done()
	if !errors.Is(dispatch.Err(), context.Canceled) || !errors.Is(context.Cause(dispatch), callerStopped) {
		t.Fatalf("dispatch after caller cancel = %v (cause %v), want %v caused by %v", dispatch.Err(), context.Cause(dispatch), context.Canceled, callerStopped)
	}
}
