package participants

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type providerConfiguredRecordingSession struct {
	*recordingSession
}

func (*providerConfiguredRecordingSession) InitialSessionConfigSent() bool { return true }

func TestSessionModelRunner_DoesNotEchoProviderOwnedInitialConfig(t *testing.T) {
	session := &providerConfiguredRecordingSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, &messages.SessionUpdateConfig{
		Instructions: "be brief", Model: "gpt-realtime",
		Tools: []messages.ToolDefinition{{Name: "lookup_weather"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()
	if !session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("provider-owned", "gpt-realtime")}) {
		t.Fatal("failed to enqueue SESSION.CREATED")
	}
	forwarded, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
	if !ok {
		t.Fatal("context cancelled waiting for forwarded SESSION.CREATED")
	}
	if forwarded.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("forwarded type = %s, want %s", forwarded.Type, messages.StreamTypeSessionCreated)
	}
	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("provider messages = %#v, want no echoed initial SESSION.UPDATE", sent)
	}
}

func TestSessionModelRunner_PreservesLaterSessionUpdateAndAcknowledgement(t *testing.T) {
	session := &providerConfiguredRecordingSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(nil, 8, &messages.SessionUpdateConfig{Tools: []messages.ToolDefinition{{Name: "lookup_weather"}}})
	ctx := context.Background()
	failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{Type: messages.StreamTypeSessionUpdate, Value: messages.NewSessionUpdateValue(runner.sessionConfig)})
	if failure.Type != "" || deferred || accepted {
		t.Fatalf("later SESSION.UPDATE outcome = (%#v, %t, %t), want accepted ordinary update", failure, deferred, accepted)
	}
	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeSessionUpdate {
		t.Fatalf("later provider messages = %#v, want one SESSION.UPDATE", sent)
	}
	runner.forwardSessionMessageState(ctx, session, newSessionResponseState(), messages.StreamMessage{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("provider-owned")})
	acknowledgement, ok := runner.DeltaOutbox.Read()
	if !ok || acknowledgement.Type != messages.StreamTypeSessionUpdated {
		t.Fatalf("forwarded acknowledgement = %#v, ok=%t; want SESSION.UPDATED", acknowledgement, ok)
	}
}

func TestSessionModelRunnerWaitingAudioIngressBackpressuresUntilCapacity(t *testing.T) {
	runner := NewSessionModelRunner(nil, 8, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < cap(runner.sessionInputInbox); i++ {
		if err := runner.EnqueueSessionAudioInput(ctx, []byte{byte(i)}); err != nil {
			t.Fatalf("fill ordered session ingress at %d: %v", i, err)
		}
	}
	admitted := make(chan error, 1)
	go func() {
		admitted <- runner.EnqueueSessionAudioInputWithPolicyWaiting(ctx, []byte{0xff}, messages.SessionAudioInputPolicyDefault)
	}()
	<-runner.sessionInputInbox
	if err := <-admitted; err != nil {
		t.Fatalf("waiting audio admission = %v, want capacity backpressure then success", err)
	}
}

func TestModelRunner_ExplicitSessionAudioPolicyControlsCancellation(t *testing.T) {
	tests := []struct {
		name          string
		policy        messages.SessionAudioInputPolicy
		wantCancel    bool
		wantSentCount int
	}{
		{
			name:          "peer audio does not cancel",
			policy:        messages.SessionAudioInputPolicyDoNotInterrupt,
			wantCancel:    false,
			wantSentCount: 1,
		},
		{
			name:          "customer audio cancels",
			policy:        messages.SessionAudioInputPolicyInterrupt,
			wantCancel:    true,
			wantSentCount: 2,
		},
		{
			name:          "unknown audio origin cancels by default",
			policy:        messages.SessionAudioInputPolicy("unclassified"),
			wantCancel:    true,
			wantSentCount: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			session := newRecordingSession()
			runner := NewSessionModelRunner(nil, 8, nil)
			state := newInFlightRunState(t, session, runner, "resp-policy")

			if err := runner.EnqueueSessionAudioInputWithPolicy(ctx, []byte{1, 2, 3, 4}, test.policy); err != nil {
				t.Fatalf("EnqueueSessionAudioInputWithPolicy: %v", err)
			}
			input := <-runner.sessionInputInbox
			if input.kind != sessionInputAudio {
				t.Fatalf("queued session input kind = %d, want audio", input.kind)
			}
			if err := runner.forwardSessionAudioInputWithState(ctx, session, input.audio, state); err != nil {
				t.Fatalf("forwardSessionAudioInputWithState: %v", err)
			}

			assertPolicyAudioForwarded(t, session.sentMessages(), test.wantSentCount, test.wantCancel)
			if state.responseInFlight != true {
				t.Fatalf("response state = %+v, want response to remain in flight until provider MESSAGE.END", state)
			}
		})
	}
}

// assertPolicyAudioForwarded checks the provider sends for one policy case:
// the expected count, at most the expected cancellation, and the original PCM
// as the final AUDIO.DELTA.
func assertPolicyAudioForwarded(t *testing.T, sent []messages.StreamMessage, wantSentCount int, wantCancel bool) {
	t.Helper()
	if len(sent) != wantSentCount {
		t.Fatalf("sent %d messages = %#v, want %d", len(sent), sent, wantSentCount)
	}
	cancelCount := 0
	for _, msg := range sent {
		if msg.Type == messages.StreamTypeResponseCancel {
			cancelCount++
		}
	}
	if (cancelCount == 1) != wantCancel {
		t.Fatalf("RESPONSE.CANCEL count = %d, want cancel=%t", cancelCount, wantCancel)
	}
	if sent[len(sent)-1].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("last sent message = %s, want AUDIO.DELTA", sent[len(sent)-1].Type)
	}
	value, ok := sent[len(sent)-1].Value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("last sent value = %T, want *messages.AudioDeltaValue", sent[len(sent)-1].Value)
	}
	if got := value.Content; string(got) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("forwarded PCM = %v, want [1 2 3 4]", got)
	}
}

func TestModelRunner_DrainSessionAudioForwardsQueuedFrames(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	runner.UserAudioInbox <- []byte{4, 5, 6}

	responseInFlight := false
	responseCancelSent := false
	if err := runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent); err != nil {
		t.Fatalf("drain queued audio: %v", err)
	}

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("drained sends = %#v, want one AUDIO.DELTA", sent)
	}
	value, ok := sent[0].Value.(*messages.AudioDeltaValue)
	if !ok || string(value.Content) != string([]byte{4, 5, 6}) {
		t.Fatalf("drained audio = %#v, want original frame", sent[0].Value)
	}

	close(runner.UserAudioInbox)
	if err := runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent); err != nil {
		t.Fatalf("drain closed audio inbox: %v", err)
	}
}

func TestModelRunner_ExplicitInterruptPolicyDoesNotCancelToolContinuation(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := newInFlightRunState(t, session, runner, "resp-continuation-policy")
	state.awaitingContinuation = true

	if err := runner.EnqueueSessionAudioInputWithPolicy(ctx, []byte{7, 7, 7}, messages.SessionAudioInputPolicyInterrupt); err != nil {
		t.Fatalf("EnqueueSessionAudioInputWithPolicy: %v", err)
	}
	input := <-runner.sessionInputInbox
	if input.kind != sessionInputAudio {
		t.Fatalf("queued session input kind = %d, want audio", input.kind)
	}
	if err := runner.forwardSessionAudioInputWithState(ctx, session, input.audio, state); err != nil {
		t.Fatalf("forwardSessionAudioInputWithState: %v", err)
	}

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("tool continuation sends = %#v, want only AUDIO.DELTA", sent)
	}
	if state.responseCancelSent {
		t.Fatalf("tool continuation state = %+v, want cancellation exemption preserved", state)
	}
}
