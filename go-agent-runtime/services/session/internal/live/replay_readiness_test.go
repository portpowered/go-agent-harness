package live

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type recordingAudioInputSender struct {
	payload []byte
	policy  messages.SessionAudioInputPolicy
}

func (s *recordingAudioInputSender) sendAudioInput(_ context.Context, payload []byte, policy messages.SessionAudioInputPolicy) error {
	s.payload = append([]byte(nil), payload...)
	s.policy = policy
	return nil
}

func TestLoopAudioOutboundUsesOrderedPCMInputPolicy(t *testing.T) {
	sender := &recordingAudioInputSender{}
	var admitted sharedaudio.PCMFrame
	outbound := &loopAudioOutbound{sender: sender, onAdmit: func(frame sharedaudio.PCMFrame) { admitted = frame }}
	want := []int16{1, -2, 32767, -32768}
	frame := sharedaudio.PCMFrame{Samples: want, Format: sharedaudio.PCM16DeviceFormat(16000), StreamID: "capture", Sequence: 7}

	if err := outbound.WriteFrame(context.Background(), frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	got, err := codec.DecodePCM16(sender.payload)
	if err != nil {
		t.Fatalf("DecodePCM16: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("encoded samples = %v, want %v", got, want)
	}
	if sender.policy != messages.SessionAudioInputPolicyDefault {
		t.Fatalf("policy = %q, want default interrupting policy", sender.policy)
	}
	if !slices.Equal(admitted.Samples, want) || admitted.StreamID != frame.StreamID || admitted.Sequence != frame.Sequence {
		t.Fatalf("admitted frame = %+v, want metadata-preserving copy of %+v", admitted, frame)
	}
}

type replayReadinessSession struct {
	*testSession
	media sharedaudio.MediaEndpoints
}

func (s *replayReadinessSession) RTCMedia() sharedaudio.MediaEndpoints { return s.media }

type replayReadinessInferencer struct{ session messages.Session }

func (i *replayReadinessInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func awaitSessionOpen(t testing.TB, events <-chan session.LiveEvent) []session.LiveEvent {
	t.Helper()
	var got []session.LiveEvent
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before SESSION.OPEN")
			}
			got = append(got, event)
			if event.Kind == string(session.LiveEventStarted) {
				continue
			}
			if event.Kind == string(messages.StreamTypeSessionOpen) {
				return got
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for SESSION.OPEN")
		}
	}
}

func waitForSentText(t testing.TB, provider *testSession, text string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for !provider.hasText(text) {
		select {
		case <-deadline.C:
			t.Fatalf("text %q was not delivered", text)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func collectTestLiveEvents(events <-chan session.LiveEvent) []session.LiveEvent {
	var got []session.LiveEvent
	for event := range events {
		got = append(got, event)
	}
	return got
}

func queueProviderMessages(t testing.TB, provider *testSession, messagesToQueue []messages.StreamMessage) {
	t.Helper()
	for _, message := range messagesToQueue {
		if !provider.receive.Write(context.Background(), message) {
			t.Fatalf("queue provider message %s", message.Type)
		}
	}
}

func assertEmptyResponseEvents(t testing.TB, events []session.LiveEvent) {
	t.Helper()
	faultIndex, terminalIndex := -1, -1
	for index, event := range events {
		switch event.Kind {
		case string(session.LiveEventLiveness):
			faultIndex = index
			if event.Liveness == nil || event.Liveness.Classification != "silent_provider_empty_response" || event.Liveness.ResponseID != "response-empty" {
				t.Fatalf("liveness event = %+v", event)
			}
		case string(session.LiveEventTerminal):
			terminalIndex = index
			if event.Terminal == nil || event.Terminal.Classification != "silent_provider_empty_response" || event.Terminal.TerminalReason != messages.TerminalReasonTerminalFailure {
				t.Fatalf("terminal event = %+v", event)
			}
		}
	}
	if faultIndex < 0 || terminalIndex < 0 || faultIndex >= terminalIndex {
		t.Fatalf("event order = fault %d, terminal %d, events %+v", faultIndex, terminalIndex, events)
	}
}

func TestReplayWaitsForSessionUpdatedBeforeFirstPCM(t *testing.T) {
	provider := newTestSession()
	frameSeen := make(chan struct{})
	frameOnce := frameSeen
	media := sharedaudio.NewSessionMediaAtRate(func(context.Context, sharedaudio.PCMFrame) error {
		select {
		case <-frameOnce:
		default:
			close(frameOnce)
		}
		return nil
	}, 24000)

	service := New(Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return &replayReadinessInferencer{session: &replayReadinessSession{testSession: provider, media: media.Endpoints()}}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "replay-readiness",
		ReplayPlan: &session.LiveReplayPlan{
			WaitForSessionUpdated: true,
			AudioTurns:            []session.LiveReplayAudioTurn{{Chunks: [][]int16{{1, 2, 3}}}},
		},
		FinishAfterResponse: true,
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-frameSeen:
		t.Fatal("replay admitted PCM before the provider session.updated boundary")
	case <-time.After(20 * time.Millisecond):
	}

	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider", "audio")},
		{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("provider")},
	} {
		if !provider.receive.Write(context.Background(), msg) {
			t.Fatalf("queue provider message %s", msg.Type)
		}
	}
	select {
	case <-frameSeen:
	case <-time.After(time.Second):
		t.Fatal("replay did not admit PCM after session.updated")
	}

	cause := errors.New("stop readiness fixture")
	handle.Cancel(cause)
	if err := handle.Wait(); !errors.Is(err, cause) {
		t.Fatalf("Wait = %v, want cancellation cause", err)
	}
}
