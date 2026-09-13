package agentruntime

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionFailureProjection_AdapterRetainsOneInvocationService(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	first := errors.New("first provider failure")
	second := errors.New("late provider failure")
	var callbacks atomic.Int32
	var observations []sessionTerminalObservation
	observer.terminalObserver = func(observation sessionTerminalObservation) bool {
		callbacks.Add(1)
		if snapshot := observer.failureSnapshot(); snapshot == nil || snapshot.classification != observation.Classification {
			t.Errorf("callback snapshot = %#v, want %q accepted facts", snapshot, observation.Classification)
		}
		observations = append(observations, observation)
		return true
	}

	observer.captureFailureFromError(&messages.ErrorValue{Classification: "first", Err: first})
	cached := fi(observer).Failure
	if got := fi(observer).Failure; got != cached {
		t.Fatalf("cached service changed: got=%T want=%T", got, cached)
	}
	if snapshot := cached.Snapshot(); snapshot == nil || snapshot.Facts.Classification != "first" {
		t.Fatalf("cached service snapshot = %#v", snapshot)
	}
	observer.captureFailureFromError(&messages.ErrorValue{Classification: "second", Err: second})

	if callbacks.Load() != 1 {
		t.Fatalf("failure callbacks = %d, want one", callbacks.Load())
	}
	if len(observations) != 1 || !errors.Is(observations[0].Err, first) {
		t.Fatalf("callback observations = %#v, want original first error", observations)
	}
	snapshot := observer.failureSnapshot()
	if snapshot == nil || snapshot.classification != "first" {
		t.Fatalf("retained snapshot = %#v, want first accepted facts", snapshot)
	}

	observer.clearFailure()
	if observer.failureSnapshot() != nil {
		t.Fatal("clear retained the invocation failure")
	}
	observer.captureFailureFromError(&messages.ErrorValue{Classification: "second", Err: second})
	if callbacks.Load() != 2 || len(observations) != 2 || !errors.Is(observations[1].Err, second) {
		t.Fatalf("callbacks after clear = %d, want two", callbacks.Load())
	}
}

func TestSessionFailureProjection_NilCompatibilityInputsAreSafe(t *testing.T) {
	var observer *sessionProgressObserver
	if observer.failureSnapshot() != nil {
		t.Fatal("nil observer exposed a snapshot")
	}
	observer.clearFailure()
	observer.captureFailureFromError(nil)
	observer.captureFailureFromClose(nil)
	if observer.acceptFailureObservation(nil, nil) {
		t.Fatal("nil observer accepted failure facts")
	}
	if observer.unresolvedToolResultFailureFacts("run") != nil || observer.imageContinuationFailureFacts("run") != nil || observer.toolContinuationFailureFacts("run") != nil || observer.scheduledAudioFailureFacts("run") != nil {
		t.Fatal("nil observer projected failure facts")
	}
	observer.emitToolCallRecord(nil)
}
