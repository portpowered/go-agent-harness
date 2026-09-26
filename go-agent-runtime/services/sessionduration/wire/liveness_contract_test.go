package wire

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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

// capabilityProvider is a provider session that runs turn detection, owns
// local playback and declares its input rate.
type capabilityProvider struct {
	messages.Session
	interrupts int
}

func (*capabilityProvider) ProviderTurnDetection() bool { return true }
func (*capabilityProvider) InputAudioSampleRate() int   { return 24000 }
func (*capabilityProvider) LocalPlayback() messages.LocalPlaybackState {
	return messages.LocalPlaybackState{Active: true, Level: 1234}
}
func (p *capabilityProvider) InterruptLocalPlayback(context.Context) bool {
	p.interrupts++
	return true
}

// The recording, strict-replay and live-recorder paths wrap the provider in a
// session recorder and the admission session. The runner's local barge-in
// reads turn detection, local playback and the input rate through both.
func TestAdmissionSessionForwardsBargeInCapabilities(t *testing.T) {
	service := NewService()
	provider := &capabilityProvider{Session: newPublicSession()}
	recorded := gwtesting.NewSessionRecorder(provider)
	var session messages.Session = service.NewAdmissionSession(context.Background(), recorded, service.NewEventAdmission(), nil)
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close admission session: %v", err)
		}
	})
	detector, ok := session.(messages.SessionTurnDetection)
	if !ok || !detector.ProviderTurnDetection() {
		t.Fatal("provider turn detection was not forwarded")
	}
	format, ok := session.(messages.SessionInputFormat)
	if !ok || format.InputAudioSampleRate() != 24000 {
		t.Fatal("input sample rate was not forwarded")
	}
	playback, ok := session.(messages.SessionLocalPlayback)
	if !ok || playback.LocalPlayback().Level != 1234 || !playback.InterruptLocalPlayback(context.Background()) || provider.interrupts != 1 {
		t.Fatal("local playback was not forwarded")
	}
}
