package wire

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestPublicLivenessIgnoresStaleResponseCancellation(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(180, 0), time.Millisecond)
	controller, err := NewService().Begin(context.Background(), sessionduration.Options{
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
	controller, err := NewService().Begin(context.Background(), sessionduration.Options{
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
	controller, err := NewService().Begin(context.Background(), sessionduration.Options{
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
	controller, err := NewService().Begin(context.Background(), sessionduration.Options{
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

// capabilityProvider is a provider session with every barge-in capability,
// response requests and complete messages. It records the calls it receives.
type capabilityProvider struct {
	messages.Session
	calls []string
}

func (*capabilityProvider) ProviderTurnDetection() bool { return true }
func (*capabilityProvider) InputAudioSampleRate() int   { return 24000 }
func (*capabilityProvider) LocalPlayback() messages.LocalPlaybackState {
	return messages.LocalPlaybackState{Active: true, Level: 1234}
}
func (p *capabilityProvider) InterruptLocalPlayback(context.Context) bool {
	p.calls = append(p.calls, "interrupt playback")
	return true
}
func (p *capabilityProvider) Send(_ context.Context, msg messages.StreamMessage) bool {
	p.calls = append(p.calls, string(msg.Type))
	return true
}
func (p *capabilityProvider) RequestResponse(context.Context) messages.SessionSendOutcome {
	p.calls = append(p.calls, "request response")
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}
func (p *capabilityProvider) SendMessage(context.Context, messages.Message) bool {
	p.calls = append(p.calls, "complete message")
	return true
}

// connectAdmissionChain wraps provider the way the recording, strict-replay
// and live-recorder paths do: a session recorder inside the admission session.
func connectAdmissionChain(t *testing.T, provider messages.Session) messages.Session {
	t.Helper()
	service := NewService()
	recorded, err := gwtesting.NewSessionRecorder(t.Context(), provider)
	if err != nil {
		t.Fatalf("NewSessionRecorder: %v", err)
	}
	var session messages.Session = service.NewAdmissionSession(t.Context(), recorded, service.NewEventAdmission(), nil)
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close admission session: %v", err)
		}
	})
	return session
}

// The runner's barge-in reaches the provider through the recorder and the
// admission session: turn detection, input rate, playback, response cancel,
// response requests and complete messages.
func TestAdmissionChainRelaysBargeInCapabilities(t *testing.T) {
	provider := &capabilityProvider{Session: newPublicSession()}
	session := connectAdmissionChain(t, provider)
	ctx := t.Context()
	capable, ok := session.(messages.BargeInCapableSession)
	if !ok || !capable.ProviderTurnDetection() || capable.InputAudioSampleRate() != 24000 || capable.LocalPlayback().Level != 1234 {
		t.Fatal("barge-in answers were not relayed")
	}
	if !capable.InterruptLocalPlayback(ctx) || !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCancel}) {
		t.Fatal("playback interrupt or response cancel was not relayed")
	}
	if !messages.RequestSessionResponse(ctx, session).OK() || !messages.SendSessionMessage(ctx, session, messages.Message{ToolCallID: "call"}) {
		t.Fatal("response request or complete message was not relayed")
	}
	// The recorder records a response request as the RESPONSE.CREATE it sends.
	want := []string{"interrupt playback", string(messages.StreamTypeResponseCancel), string(messages.StreamTypeResponseCreate), "complete message"}
	if !slices.Equal(provider.calls, want) {
		t.Fatalf("provider calls = %v, want %v", provider.calls, want)
	}
}

// The admission chain advertises nothing the provider lacks.
func TestAdmissionChainDoesNotAdvertiseMissingCapabilities(t *testing.T) {
	session := connectAdmissionChain(t, newPublicSession())
	ctx := t.Context()
	capable, ok := session.(messages.BargeInCapableSession)
	if !ok {
		t.Fatal("admission chain does not expose the barge-in capabilities")
	}
	if capable.ProviderTurnDetection() || capable.InputAudioSampleRate() != 0 || capable.LocalPlayback().Active || capable.InterruptLocalPlayback(ctx) {
		t.Fatal("a barge-in capability the provider lacks was advertised")
	}
	if messages.SupportsSessionResponseRequests(session) || messages.SupportsSessionMessages(session) || messages.SupportsSessionMessagesWithoutResponse(session) {
		t.Fatal("response requests or complete messages were advertised")
	}
	if _, ok := messages.SessionMedia(session); ok {
		t.Fatal("media was advertised")
	}
}

// The recorder and the admission session each relay provider messages through
// their own goroutine. SyncReceive must publish every message the provider had
// queued through both relays before it returns.
func TestAdmissionSessionSyncReceivePublishesQueuedProviderMessages(t *testing.T) {
	service := NewService()
	provider := newPublicSession()
	recorded, err := gwtesting.NewSessionRecorder(t.Context(), provider)
	if err != nil {
		t.Fatalf("NewSessionRecorder: %v", err)
	}
	var session messages.Session = service.NewAdmissionSession(t.Context(), recorded, service.NewEventAdmission(), nil)
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close admission session: %v", err)
		}
	})
	syncer, ok := session.(messages.SessionReceiveSyncer)
	if !ok {
		t.Fatal("admission session does not expose SyncReceive")
	}
	for round := range 50 {
		id := fmt.Sprintf("resp-%d", round)
		provider.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: id})
		syncer.SyncReceive(context.Background())
		if got, ok := session.Receive().Read(); !ok || got.ResponseID != id {
			t.Fatalf("round %d: message %q was still behind a relay after SyncReceive", round, id)
		}
	}
}
