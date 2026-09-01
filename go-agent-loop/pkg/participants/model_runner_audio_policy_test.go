package participants

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
			if err := runner.drainSessionAudioWithState(ctx, session, state); err != nil {
				t.Fatalf("drainSessionAudioWithState: %v", err)
			}

			sent := session.sentMessages()
			if len(sent) != test.wantSentCount {
				t.Fatalf("sent %d messages = %#v, want %d", len(sent), sent, test.wantSentCount)
			}
			cancelCount := 0
			for _, msg := range sent {
				if msg.Type == messages.StreamTypeResponseCancel {
					cancelCount++
				}
			}
			if (cancelCount == 1) != test.wantCancel {
				t.Fatalf("RESPONSE.CANCEL count = %d, want cancel=%t", cancelCount, test.wantCancel)
			}
			if sent[len(sent)-1].Type != messages.StreamTypeAudioDelta {
				t.Fatalf("last sent message = %s, want AUDIO.DELTA", sent[len(sent)-1].Type)
			}
			if got := sent[len(sent)-1].Value.(*messages.AudioDeltaValue).Content; string(got) != string([]byte{1, 2, 3, 4}) {
				t.Fatalf("forwarded PCM = %v, want [1 2 3 4]", got)
			}
			if state.responseInFlight != true {
				t.Fatalf("response state = %+v, want response to remain in flight until provider MESSAGE.END", state)
			}
		})
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
	if err := runner.drainSessionAudioWithState(ctx, session, state); err != nil {
		t.Fatalf("drainSessionAudioWithState: %v", err)
	}

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("tool continuation sends = %#v, want only AUDIO.DELTA", sent)
	}
	if state.responseCancelSent {
		t.Fatalf("tool continuation state = %+v, want cancellation exemption preserved", state)
	}
}
