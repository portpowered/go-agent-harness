package wire

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestPublicLivenessIgnoresStaleResponseCancellation(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(180, 0), time.Millisecond)
	controller, err := NewService().Begin(sessionduration.Options{
		Clock:    clock,
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "current"})
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Role: messages.RoleUser, ResponseID: "stale"})
	clock.AdvanceBy(6 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		var typed *sessionduration.LivenessError
		if !errors.Is(err, sessionduration.ErrProviderLivenessTimeout) || !errors.As(err, &typed) || typed.ResponseID != "current" {
			t.Fatalf("stale cancellation liveness result = %v, want timeout for current response", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale response cancellation disarmed the current watchdog")
	}
}

func TestPublicLivenessDoesNotClassifyProviderCancellationAsEmpty(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(240, 0), time.Millisecond)
	controller, err := NewService().Begin(sessionduration.Options{
		Clock:    clock,
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "cancelled"})
	controller.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "cancelled",
		Value: &messages.MessageEndValue{
			Status:         " CANCELLED ",
			TerminalReason: messages.TerminalReasonPartialOutput,
			OutputState:    messages.TerminalOutputNone,
		},
	})
	clock.AdvanceBy(6 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		t.Fatalf("provider cancellation was reported as a liveness failure: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPublicLivenessDoesNotClassifyReportedUsageAsEmpty(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(300, 0), time.Millisecond)
	controller, err := NewService().Begin(sessionduration.Options{
		Clock:    clock,
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "billed"})
	controller.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "billed",
		Value: &messages.MessageEndValue{
			Status:         "incomplete",
			TerminalReason: messages.TerminalReasonPartialOutput,
			OutputState:    messages.TerminalOutputNone,
			Usage:          messages.TokenUsage{CompletionTokens: 1},
		},
	})
	clock.AdvanceBy(6 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		t.Fatalf("provider response with completion usage was reported empty: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPublicLivenessUsesCurrentResponseForCancellationWithoutID(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(360, 0), time.Millisecond)
	controller, err := NewService().Begin(sessionduration.Options{
		Clock:    clock,
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "cancelled"})
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Role: messages.RoleUser})
	clock.AdvanceBy(6 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		t.Fatalf("ID-less provider cancellation left liveness armed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "next"})
	clock.AdvanceBy(6 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		var typed *sessionduration.LivenessError
		if !errors.Is(err, sessionduration.ErrProviderLivenessTimeout) || !errors.As(err, &typed) || typed.ResponseID != "next" {
			t.Fatalf("next response liveness result = %v, want timeout for next response", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new provider response did not rearm after ID-less cancellation")
	}
}
