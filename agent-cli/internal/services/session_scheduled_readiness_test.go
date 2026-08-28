package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type scheduledInputDispatchProbe struct {
	audio  [][]byte
	events []messages.StreamMessage
}

func TestScheduledAudioCompletionErrorJoinsPrimaryAndReportsCounts(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0},
		{AfterCompletedTurns: 1},
		{AfterCompletedTurns: 2},
	})
	observer.dispatchedInputs = 2
	observer.completedScheduled = 2
	observer.turnsCompleted = 2

	primary := errors.New("provider closed cleanly")
	opts := sessionLoopOptions{CloseAfterScheduledAudio: true, observer: observer}
	err := scheduledAudioCompletionError(primary, opts)
	if !errors.Is(err, primary) {
		t.Fatalf("scheduled completion error lost primary cause: %v", err)
	}
	if !errors.Is(err, ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("scheduled completion error = %v, want incomplete sentinel", err)
	}
	var incomplete *SessionScheduledAudioIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("scheduled completion error = %v, want typed incomplete error", err)
	}
	if incomplete.Completed != 2 || incomplete.Dispatched != 2 || incomplete.Scheduled != 3 {
		t.Fatalf("scheduled completion counts = %+v, want completed=2 dispatched=2 scheduled=3", incomplete)
	}

	second := scheduledAudioCompletionError(err, opts)
	if second != err {
		t.Fatalf("scheduled completion error was wrapped more than once: first=%v second=%v", err, second)
	}
}

func TestSessionProgressObserverScheduledIncompleteFailureIncludesCountsOnce(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime")
	observer.sawSessionOpen = true
	observer.turnsCompleted = 2
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0},
		{AfterCompletedTurns: 1},
		{AfterCompletedTurns: 2},
	})
	observer.dispatchedInputs = 2
	observer.completedScheduled = 2

	err := scheduledAudioCompletionError(nil, sessionLoopOptions{
		CloseAfterScheduledAudio: true,
		observer:                 observer,
	})
	err = observer.finish(err)
	_ = observer.finish(err)

	failures := sink.events(SessionDiagnosticEventFailure)
	if len(failures) != 1 {
		t.Fatalf("scheduled incomplete failure records = %d, want exactly one", len(failures))
	}
	fields := failures[0].Fields
	if fields[fieldClassification] != SessionScheduledAudioClassification {
		t.Fatalf("scheduled incomplete classification = %q, want %q", fields[fieldClassification], SessionScheduledAudioClassification)
	}
	want := map[string]string{
		SessionDiagnosticFieldCompletedTurnCount:   "2",
		SessionDiagnosticFieldDispatchedInputCount: "2",
		SessionDiagnosticFieldScheduledInputCount:  "3",
	}
	for key, wantValue := range want {
		if got := fields[key]; got != wantValue {
			t.Fatalf("scheduled incomplete field %q = %q, want %q; fields=%v", key, got, wantValue, fields)
		}
	}
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
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("follow-on answer"),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: &messages.MessageEndValue{Type: "message_end", Status: "completed"},
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
