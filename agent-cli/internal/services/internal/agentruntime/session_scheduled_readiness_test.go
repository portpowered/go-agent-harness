package agentruntime

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

func TestSessionProgressObserverActiveResponseReleasesOnlyFollowingTurn(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduledAudioDispatch = ScheduledAudioDispatchActiveResponse
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
		{AfterCompletedTurns: 2, PCM: []byte{3}, EndOfTurn: true},
	})

	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first scheduled input: %v", err)
	}
	if len(probe.audio) != 1 {
		t.Fatalf("initial scheduled inputs = %d, want 1", len(probe.audio))
	}

	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch active-response follow-on input: %v", err)
	}
	if len(probe.audio) != 2 || string(probe.audio[1]) != string([]byte{2}) {
		t.Fatalf("active-response dispatch = %#v, want only second turn", probe.audio)
	}
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("second response"),
	})

	// The active response boundary only releases the immediately following
	// input. The third turn remains ordered behind the second response.
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if observer.activeResponse {
		t.Fatal("terminal MESSAGE.END left the response active")
	}
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch after first response terminality: %v", err)
	}
	if len(probe.audio) != 2 {
		t.Fatalf("third turn crossed the second-response gate = %#v, want only two turns", probe.audio)
	}
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch third active-response input: %v", err)
	}
	if len(probe.audio) != 3 || string(probe.audio[2]) != string([]byte{3}) {
		t.Fatalf("third active-response dispatch = %#v, want third turn exactly once", probe.audio)
	}
}

func TestSessionProgressObserverTerminalResponseWinsBeforeActiveDispatch(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduledAudioDispatch = ScheduledAudioDispatchActiveResponse
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
	})

	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first scheduled input: %v", err)
	}
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("response"),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	if observer.activeResponse {
		t.Fatal("terminal response remained active")
	}
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch normal following turn after terminal response: %v", err)
	}
	if len(probe.audio) != 2 || string(probe.audio[1]) != string([]byte{2}) {
		t.Fatalf("terminal-wins dispatch = %#v, want second turn exactly once", probe.audio)
	}
}

func TestSessionProgressObserverCountsCancelledScheduledResponseDisposition(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduledAudioDispatch = ScheduledAudioDispatchActiveResponse
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first scheduled input: %v", err)
	}

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-cancelled",
		Value:      messages.NewMessageStartValue(),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch interrupting scheduled input: %v", err)
	}

	// No output crossed the cancellation boundary. The cancelled response is
	// still a terminal disposition for its already-dispatched input, but it is
	// not an admitted assistant turn.
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-cancelled",
		Role:       messages.RoleAssistant,
		Value: messages.NewMessageEndValueWithTerminal(
			messages.TokenUsage{},
			messages.TerminalReasonPartialOutput,
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputNone,
		),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-replacement",
		Value:      messages.NewMessageStartValue(),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "resp-replacement",
		Role:       messages.RoleAssistant,
		Value:      messages.NewTextDeltaValue("replacement answer"),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-replacement",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	if observer.turnsCompleted != 1 {
		t.Fatalf("ordinary completed turns = %d, want replacement only", observer.turnsCompleted)
	}
	if completed, dispatched, scheduled := observer.scheduledAudioCounts(); completed != 2 || dispatched != 2 || scheduled != 2 {
		t.Fatalf("scheduled terminal dispositions = %d/%d/%d, want completed=2 dispatched=2 scheduled=2", completed, dispatched, scheduled)
	}
	if !observer.scheduledAudioComplete() {
		t.Fatal("cancelled and replacement scheduled responses did not satisfy clean completion")
	}
}

func TestSessionProgressObserverResolvedBargeLifecycleReleasesThirdTurnAfterReplacement(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduledAudioDispatch = ScheduledAudioDispatchActiveResponse
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
		{AfterCompletedTurns: 2, PCM: []byte{3}, EndOfTurn: true},
	})

	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first scheduled input: %v", err)
	}
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-first",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageStartValue(),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch barging scheduled input: %v", err)
	}

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-first",
		Role:       messages.RoleAssistant,
		Value: messages.NewMessageEndValueWithTerminal(
			messages.TokenUsage{},
			messages.TerminalReasonPartialOutput,
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputNone,
		),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-second",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageStartValue(),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch while replacement response is active: %v", err)
	}
	if len(probe.audio) != 2 {
		t.Fatalf("third scheduled input crossed cancelled-response lookahead: %#v", probe.audio)
	}
	if observer.turnsCompleted != 0 {
		t.Fatalf("cancelled response advanced ordinary turns = %d, want 0", observer.turnsCompleted)
	}
	if observer.completedScheduled != 1 {
		t.Fatalf("resolved scheduled lifecycles = %d, want cancelled first lifecycle only", observer.completedScheduled)
	}

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "resp-second",
		Role:       messages.RoleAssistant,
		Value:      messages.NewTextDeltaValue("replacement response"),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-second",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch third scheduled input after replacement: %v", err)
	}
	if len(probe.audio) != 3 || string(probe.audio[2]) != string([]byte{3}) {
		t.Fatalf("resolved lifecycle dispatch = %#v, want third input exactly once", probe.audio)
	}

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-third",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageStartValue(),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "resp-third",
		Role:       messages.RoleAssistant,
		Value:      messages.NewTextDeltaValue("third response"),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-third",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	if observer.turnsCompleted != 2 {
		t.Fatalf("ordinary completed turns = %d, want two admitted replacements", observer.turnsCompleted)
	}
	if completed, dispatched, scheduled := observer.scheduledAudioCounts(); completed != 3 || dispatched != 3 || scheduled != 3 {
		t.Fatalf("final scheduled counts = %d/%d/%d, want completed=3 dispatched=3 scheduled=3", completed, dispatched, scheduled)
	}
	if !observer.scheduledAudioComplete() {
		t.Fatal("resolved three-turn schedule was not marked complete")
	}
}

func TestSessionProgressObserverCompletionGatedIgnoresActiveResponse(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
	})

	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first completion-gated input: %v", err)
	}
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch completion-gated input: %v", err)
	}
	if len(probe.audio) != 1 {
		t.Fatalf("completion-gated scheduler dispatched %d inputs while response active, want 1", len(probe.audio))
	}
}

func TestSessionProgressObserverFirstScheduledOffsetWaitsForPromptResponse(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduledAudioDispatch = ScheduledAudioDispatchActiveResponse
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 1, PCM: []byte{1}, EndOfTurn: true}})

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-prompt",
		Value:      messages.NewMessageStartValue(),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch while prompt response is active: %v", err)
	}
	if len(probe.audio) != 0 {
		t.Fatalf("first scheduled input crossed active prompt boundary: %#v", probe.audio)
	}
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "resp-prompt",
		Role:       messages.RoleAssistant,
		Value:      messages.NewTextDeltaValue("prompt response"),
	})

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-prompt",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch after prompt response: %v", err)
	}
	if len(probe.audio) != 1 || string(probe.audio[0]) != string([]byte{1}) {
		t.Fatalf("first scheduled input after prompt = %#v, want one [1] frame", probe.audio)
	}
}

func TestSessionProgressObserverResponseIdentityRejectsOutOfOrderTerminal(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduledAudioDispatch = ScheduledAudioDispatchActiveResponse
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
		{AfterCompletedTurns: 2, PCM: []byte{3}, EndOfTurn: true},
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first scheduled input: %v", err)
	}

	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: "resp-old", Value: messages.NewMessageStartValue()})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch while first response is active: %v", err)
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: "resp-current", Value: messages.NewMessageStartValue()})
	if observer.activeResponseID != "resp-current" {
		t.Fatalf("active response ID = %q, want resp-current", observer.activeResponseID)
	}
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch while replacement response is active: %v", err)
	}
	if len(probe.audio) != 2 {
		t.Fatalf("active-response dispatches = %#v, want first two inputs", probe.audio)
	}

	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "resp-old", Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if !observer.activeResponse || observer.activeResponseID != "resp-current" || observer.turnsCompleted != 0 {
		t.Fatalf("late old terminal changed lifecycle: active=%t id=%q turns=%d", observer.activeResponse, observer.activeResponseID, observer.turnsCompleted)
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, ResponseID: "resp-current", Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("current response")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "resp-current", Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if observer.activeResponse || observer.turnsCompleted != 1 {
		t.Fatalf("current terminal lifecycle = active:%t turns:%d, want inactive/1", observer.activeResponse, observer.turnsCompleted)
	}
}

func TestSessionProgressObserverScheduledAudioCountsOnlyItsOwnCompletedTurns(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	probe := &scheduledInputDispatchProbe{}

	// A prompt/seed response belongs to the session, not to the scheduled
	// sequence. The first dispatched input establishes the schedule baseline.
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("prompt response"),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 1, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 2, PCM: []byte{2}, EndOfTurn: true},
	})

	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch first scheduled input: %v", err)
	}
	if completed, dispatched, scheduled := observer.scheduledAudioCounts(); completed != 0 || dispatched != 1 || scheduled != 2 {
		t.Fatalf("initial scheduled counts = %d/%d/%d, want 0/1/2", completed, dispatched, scheduled)
	}

	for index := 0; index < 2; index++ {
		observer.observe(messages.StreamMessage{
			Type:  messages.StreamTypeMessageStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageStartValue(),
		})
		observer.observe(messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue("scheduled response"),
		})
		observer.observe(messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
		if index == 0 {
			if completed, dispatched, scheduled := observer.scheduledAudioCounts(); completed != 1 || dispatched != 1 || scheduled != 2 {
				t.Fatalf("after first scheduled response counts = %d/%d/%d, want 1/1/2", completed, dispatched, scheduled)
			}
			if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
				t.Fatalf("dispatch second scheduled input: %v", err)
			}
		}
	}

	if completed, dispatched, scheduled := observer.scheduledAudioCounts(); completed != 2 || dispatched != 2 || scheduled != 2 {
		t.Fatalf("final scheduled counts = %d/%d/%d, want 2/2/2", completed, dispatched, scheduled)
	}
	if !observer.scheduledAudioComplete() {
		t.Fatal("completed scheduled sequence was not marked complete")
	}
}

func TestScheduledAudioCompletionErrorCarriesCountsAndDiagnosticFields(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime")
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch scheduled input: %v", err)
	}
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("response"),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	err := scheduledAudioCompletionError(nil, sessionLoopOptions{
		CloseAfterScheduledAudio: true,
		observer:                 observer,
	})
	var incomplete *SessionScheduledAudioIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("scheduled completion error = %v, want typed incomplete counts", err)
	}
	if !errors.Is(err, ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("scheduled completion error = %v, want incomplete sentinel", err)
	}
	if incomplete.Completed != 1 || incomplete.Dispatched != 1 || incomplete.Scheduled != 2 {
		t.Fatalf("incomplete scheduled counts = %+v, want completed=1 dispatched=1 scheduled=2", incomplete)
	}

	if err := observer.finish(err); err == nil {
		t.Fatal("observer finish erased scheduled completion failure")
	}
	failures := sink.events(SessionDiagnosticEventFailure)
	if len(failures) != 1 {
		t.Fatalf("scheduled failure records = %d, want exactly one", len(failures))
	}
	fields := failures[0].Fields
	if fields[SessionDiagnosticFieldScheduledInputCount] != "2" ||
		fields[SessionDiagnosticFieldDispatchedInputCount] != "1" ||
		fields[SessionDiagnosticFieldCompletedTurnCount] != "1" {
		t.Fatalf("scheduled failure fields = %#v, want configured/dispatched/completed 2/1/1", fields)
	}
}
