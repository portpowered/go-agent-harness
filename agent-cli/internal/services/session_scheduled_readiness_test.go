package services

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
