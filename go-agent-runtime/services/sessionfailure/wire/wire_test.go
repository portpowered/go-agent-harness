package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
)

func TestNewServiceBindsPublicContract(t *testing.T) {
	var service sessionfailure.Service = NewService(sessionfailure.Dependencies{})
	if service == nil {
		t.Fatal("Wire returned a nil session-failure service")
	}
	if service.OutputState(sessionfailure.Progress{}) == "" {
		t.Fatal("Wire-bound service did not execute")
	}
}

func TestInvocationHelpersDelegateToIsolatedService(t *testing.T) {
	const partialOutput = "partial"

	invocation := NewInvocation(nil, sessionfailure.Dependencies{})
	if invocation == nil || invocation.Service != nil || invocation.Failure == nil {
		t.Fatalf("invocation = %#v", invocation)
	}

	progress := Progress(true, 3)
	if progress != (sessionfailure.Progress{SessionOpened: true, TurnsCompleted: 3}) {
		t.Fatalf("progress = %#v", progress)
	}
	if got := OutputStateForProgress(true, 3); got != partialOutput {
		t.Fatalf("OutputStateForProgress = %q", got)
	}
	if got := OutputState(progress); got != partialOutput {
		t.Fatalf("OutputState = %q", got)
	}

	facts := Facts(sessionfailure.ProjectionToolContinuation, "tool_continuation", progress)
	if facts.Classification != sessionfailure.ClassificationToolContinuation || facts.FailingEvent != "tool_continuation" || facts.OutputState != partialOutput {
		t.Fatalf("Facts = %#v", facts)
	}
	if got := RunFacts(nil); got != (sessionfailure.Facts{}) {
		t.Fatalf("RunFacts(nil) = %#v", got)
	}
	if got := RunFacts(&engine.StreamDeltaError{Value: &messages.ErrorValue{Message: "provider failure"}}); got.FailingEvent != string(messages.StreamTypeError) {
		t.Fatalf("RunFacts(provider failure) = %#v", got)
	}
}
