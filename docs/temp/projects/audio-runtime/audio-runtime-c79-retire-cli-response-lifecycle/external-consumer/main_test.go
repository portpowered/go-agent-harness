package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics/wire"
)

func TestExternalConsumerExercisesContinuationAndMalformedOrder(t *testing.T) {
	service := wire.NewService(sessiondiagnostics.Options{})
	if service == nil {
		t.Fatal("wire.NewService returned nil")
	}
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventEnsureScheduled, Count: 1})
	if !apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-1"}).NewResponse {
		t.Fatal("initial response was not admitted")
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-1"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolCall, ResponseID: "response-1", CallID: "call-1", ToolName: "lookup"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-1", Output: true})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolResultAccepted, CallID: "call-1"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventContinuationRequested})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, Role: sessiondiagnostics.RoleTool, CallID: "call-1"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-2"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-2"})
	continuation := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-2", Output: true, Terminal: &sessiondiagnostics.Terminal{Status: "completed"}})
	if !continuation.Candidate {
		t.Fatal("continuation did not produce an admitted candidate")
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventScheduledDisposition, ResponseID: "response-2", Disposition: sessiondiagnostics.DispositionCompleted})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseFinish, ResponseID: "response-2"})

	if _, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventContinuationRequested}); !errors.Is(err, sessiondiagnostics.ErrMalformedSequence) {
		t.Fatalf("malformed continuation error = %v, want ErrMalformedSequence", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventReset}); err != nil {
		t.Fatalf("reset after close: %v", err)
	}
}
