package services

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type scheduledInputDispatchProbe struct {
	audio  [][]byte
	events []messages.StreamMessage
}

func (p *scheduledInputDispatchProbe) SendAudioInput(_ context.Context, pcm []byte) error {
	p.audio = append(p.audio, append([]byte(nil), pcm...))
	return nil
}

func (p *scheduledInputDispatchProbe) SendSessionEvent(_ context.Context, msg messages.StreamMessage) error {
	p.events = append(p.events, msg)
	return nil
}

func TestSessionProgressObserverScheduledReadinessUsesCurrentSessionUpdate(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.requireSessionUpdated = true
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}}})

	// An acknowledgement before the current session opens cannot release the
	// current session's pending input.
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("session-old"),
	})
	if observer.scheduledAudioReady() {
		t.Fatal("session.updated before SESSION.OPEN marked scheduled audio ready")
	}

	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("session-current", "audio_inference"),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("session-old"),
	})
	if observer.scheduledAudioReady() {
		t.Fatal("stale session.updated marked the current session ready")
	}

	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("session-current"),
	})
	if !observer.scheduledAudioReady() {
		t.Fatal("matching session.updated did not release scheduled readiness")
	}

	// A new SESSION.OPEN starts a new connection/configuration round trip and
	// invalidates the previous acknowledgement.
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("session-reconnected", "audio_inference"),
	})
	if observer.scheduledAudioReady() {
		t.Fatal("reconnect reused stale scheduled readiness")
	}
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("session-current"),
	})
	if observer.scheduledAudioReady() {
		t.Fatal("acknowledgement from the prior session released the reconnect")
	}
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("session-reconnected"),
	})
	if !observer.scheduledAudioReady() {
		t.Fatal("matching reconnect acknowledgement did not release readiness")
	}
}

func TestSessionProgressObserverLifecycleWakeReleasesNextScheduledAudio(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1, 2}, EndOfTurn: true},
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first scheduled input: %v", err)
	}

	const callID = "call-scheduled-follow-on"
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue(callID, "write_file", `{"path":"result.txt"}`),
	})
	// The first MESSAGE.END closes the provider tool-call response; the next
	// non-tool boundary is observed before result acceptance in this test to
	// model provider output racing the asynchronous send acknowledgement.
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{3, 4}, EndOfTurn: true},
	})

	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch while continuation is incomplete: %v", err)
	}
	if len(probe.audio) != 1 || len(probe.events) != 1 {
		t.Fatalf("scheduled input crossed tool lifecycle gate early: audio=%d events=%d, want 1/1", len(probe.audio), len(probe.events))
	}

	observer.noteToolResultAccepted(callID)
	observer.noteToolContinuationRequested()
	select {
	case <-observer.toolLifecycleEvents():
	case <-time.After(time.Second):
		t.Fatal("tool lifecycle completion did not wake scheduled dispatch")
	}
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch after lifecycle wake: %v", err)
	}
	if len(probe.audio) != 2 || len(probe.events) != 2 {
		t.Fatalf("scheduled input was not released exactly once: audio=%d events=%d, want 2/2", len(probe.audio), len(probe.events))
	}
	if got := string(probe.audio[1]); got != string([]byte{3, 4}) {
		t.Fatalf("follow-on audio = %v, want [3 4]", probe.audio[1])
	}
}
