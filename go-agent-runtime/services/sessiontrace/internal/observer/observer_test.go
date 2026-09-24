package observer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

type observerTestTerminalService struct{}

func (observerTestTerminalService) Finalize(request sessionterminal.Request) sessionterminal.Result {
	result := sessionterminal.Result{Accounting: &sessionterminal.FinalAccounting{}}
	if request.RunError != nil && !request.UserCancelled && !request.RoomBoundCancellation {
		result.Records = append(result.Records, sessionterminal.Record{Event: sessionterminal.EventFailure})
	}
	if request.UserCancelled || request.RoomBoundCancellation {
		result.Records = append(result.Records, sessionterminal.Record{Event: sessionterminal.EventTerminal})
	}
	result.Records = append(result.Records, sessionterminal.Record{Event: sessionterminal.EventMetrics})
	return result
}

func (observerTestTerminalService) CancellationOutputState(output sessionterminal.OutputSnapshot) messages.TerminalOutputState {
	if output.AssistantOutputObserved || output.TurnsCompleted > 0 {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNone
}

type observerTestSink struct {
	mu      sync.Mutex
	records []sessiontrace.DiagnosticRecord
}

func (s *observerTestSink) RecordSessionDiagnostic(record sessiontrace.DiagnosticRecord) {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
}

func (s *observerTestSink) recordsFor(event string) []sessiontrace.DiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []sessiontrace.DiagnosticRecord
	for _, record := range s.records {
		if record.Event == event {
			records = append(records, record)
		}
	}
	return records
}

func newObserverForTest(sink sessiontrace.DiagnosticSink, options ...func(*sessiontrace.NewObserverOptions)) sessiontrace.Observer {
	config := sessiontrace.NewObserverOptions{
		Sink:            sink,
		Provider:        "provider-test",
		Model:           "model-test",
		TerminalService: observerTestTerminalService{},
	}
	for _, option := range options {
		option(&config)
	}
	return NewObserver(config)
}

func observeAssistantTextTurn(observer sessiontrace.Observer, responseID, text string) {
	observer.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: responseID,
		Value:      messages.NewMessageStartValue(),
	})
	observer.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextStart,
		Role:       messages.RoleAssistant,
		ResponseID: responseID,
		Value:      messages.NewTextStartValue(),
	})
	observer.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		Role:       messages.RoleAssistant,
		ResponseID: responseID,
		Value:      messages.NewTextDeltaValue(text),
	})
	observer.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextEnd,
		Role:       messages.RoleAssistant,
		ResponseID: responseID,
		Value:      messages.NewTextEndValue(),
	})
}

func TestObserverPublicStreamContractAccountsAndFinishes(t *testing.T) {
	sink := &observerTestSink{}
	var forwarded []messages.StreamMessageType
	observer := newObserverForTest(sink, func(options *sessiontrace.NewObserverOptions) {
		options.StreamObserver = func(message messages.StreamMessage) { forwarded = append(forwarded, message.Type) }
	})
	observer.Observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Role:  messages.RoleSystem,
		Value: messages.NewSessionOpenValue("session-1", "audio"),
	})
	observer.NoteUserTextInput("prompt")
	observer.AccountRoomAudioInput(4)
	observeAssistantTextTurn(observer, "response-1", "hello")
	end := messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	end.Status = terminalStatusCompleted
	end.TerminalReason = messages.TerminalReasonProviderAuthoredCompletion
	end.TerminalProvenance = messages.TerminalProvenanceProvider
	end.OutputState = messages.TerminalOutputComplete
	observer.Observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-1",
		Value:      end,
	})

	state := observer.State()
	if !state.SawSessionOpen || state.Provider != "provider-test" || state.Model != "model-test" {
		t.Fatalf("state identity = %+v", state)
	}
	if state.TurnsCompleted != 1 || state.InputTextBytes != 6 || state.RoomInputAudioBytes != 4 || state.OutputTextBytes != 5 {
		t.Fatalf("state accounting = %+v", state)
	}
	if !state.AssistantResponseDone || !state.AssistantOutputObserved || state.ActiveResponse {
		t.Fatalf("state completion = %+v", state)
	}
	if len(forwarded) != 6 {
		t.Fatalf("stream callback count = %d, want 6", len(forwarded))
	}
	if err := observer.Finish(nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := len(sink.recordsFor(sessiontrace.SessionDiagnosticEventTurn)); got != 1 {
		t.Fatalf("turn diagnostics = %d, want one", got)
	}
	if got := len(sink.recordsFor(sessiontrace.SessionDiagnosticEventTerminal)); got != 0 {
		t.Fatalf("normal terminal diagnostics = %d, want none", got)
	}
	if got := len(sink.recordsFor(sessiontrace.SessionDiagnosticEventMetrics)); got != 1 {
		t.Fatalf("metrics diagnostics = %d, want one", got)
	}
}

func TestObserverPublicToolContinuationAndScheduledAudioContracts(t *testing.T) {
	sink := &observerTestSink{}
	observer := newObserverForTest(sink, func(options *sessiontrace.NewObserverOptions) {
		options.RequireSessionUpdated = true
		options.ScheduledAudioDispatch = sessiontrace.ScheduledAudioDispatchActiveResponse
	})
	observer.SetToolResultsEnabledForObservation(true)
	observer.ScheduleAudioInputs([]sessiontrace.ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1, 2}, EndOfTurn: true}})
	if !observer.ScheduledAudioAwaitingConfiguration() || observer.ScheduledAudioReady() {
		t.Fatal("scheduled input became ready before SESSION.UPDATED")
	}

	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session-2", "audio")})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("session-2")})
	var sentAudio [][]byte
	var sentEvents []messages.StreamMessageType
	sender := scheduledSender{
		sendAudio: func(_ context.Context, pcm []byte) error {
			sentAudio = append(sentAudio, append([]byte(nil), pcm...))
			return nil
		},
		sendEvent: func(_ context.Context, message messages.StreamMessage) error {
			sentEvents = append(sentEvents, message.Type)
			return nil
		},
	}
	if err := observer.DispatchScheduledInputs(context.Background(), sender); err != nil {
		t.Fatalf("DispatchScheduledInputs: %v", err)
	}
	if len(sentAudio) != 1 || len(sentEvents) != 1 || scheduledCounts(observer) != [3]int{0, 1, 1} {
		t.Fatalf("scheduled dispatch audio/events/counts = %d/%d/%v", len(sentAudio), len(sentEvents), scheduledCounts(observer))
	}

	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewMessageStartValue()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewToolCallEndValue("call-1", "lookup", `{}`)})
	if !observer.HasUnresolvedToolCalls() || !observer.HasToolLifecycleObligation() {
		t.Fatal("tool call did not create a lifecycle obligation")
	}
	observer.NoteToolResultAccepted("call-1")
	observer.NoteToolContinuationRequestedFor("call-1")
	if !observer.HasPendingToolContinuations() || !observer.ContinuationResultAccepted("call-1") {
		t.Fatal("accepted tool result did not await continuation")
	}
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	observeAssistantTextTurn(observer, "continuation-response", "done")
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "continuation-response", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if observer.HasPendingToolContinuations() || observer.HasUnresolvedToolCalls() {
		t.Fatal("tool continuation remained pending after its terminal response")
	}
	if !observer.ScheduledAudioComplete() || observer.ScheduledAudioIncomplete() {
		t.Fatalf("scheduled completion = complete:%v incomplete:%v", observer.ScheduledAudioComplete(), observer.ScheduledAudioIncomplete())
	}
}

func TestObserverPublicFailureAndCancellationBoundaries(t *testing.T) {
	sink := &observerTestSink{}
	observer := newObserverForTest(sink)
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session-3", "audio")})
	observer.Observe(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValueWithTerminal("provider failed", "provider_error", messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputNone),
	})
	if observer.State().Failure == nil {
		t.Fatal("terminal provider error was not retained")
	}
	if err := observer.Finish(errors.New("provider failed")); err == nil {
		t.Fatal("Finish accepted an independent provider failure")
	}
	if got := len(sink.recordsFor(sessiontrace.SessionDiagnosticEventFailure)); got != 1 {
		t.Fatalf("failure diagnostics = %d, want one", got)
	}

	intent := &testCancellationIntent{}
	cancelSink := &observerTestSink{}
	cancelled := newObserverForTest(cancelSink, func(options *sessiontrace.NewObserverOptions) {
		options.CancellationIntent = intent
	})
	intent.MarkSIGINT()
	if !cancelled.CancellationClean(context.Canceled) {
		t.Fatal("SIGINT cancellation was not classified as clean")
	}
	if err := cancelled.Finish(context.Canceled); err != nil {
		t.Fatalf("clean cancellation Finish: %v", err)
	}
	if got := len(cancelSink.recordsFor(sessiontrace.SessionDiagnosticEventTerminal)); got != 1 {
		t.Fatalf("cancellation terminal diagnostics = %d, want one", got)
	}
}

type scheduledSender struct {
	sendAudio func(context.Context, []byte) error
	sendEvent func(context.Context, messages.StreamMessage) error
}

type testCancellationIntent struct{ marked bool }

func (i *testCancellationIntent) MarkSIGINT() {
	if i != nil {
		i.marked = true
	}
}

func (i *testCancellationIntent) SIGINTReceived() bool {
	return i != nil && i.marked
}

func (s scheduledSender) SendAudioInput(ctx context.Context, pcm []byte) error {
	return s.sendAudio(ctx, pcm)
}

func (s scheduledSender) SendSessionEvent(ctx context.Context, message messages.StreamMessage) error {
	return s.sendEvent(ctx, message)
}

func scheduledCounts(observer sessiontrace.Observer) [3]int {
	completed, dispatched, scheduled := observer.ScheduledAudioCounts()
	return [3]int{completed, dispatched, scheduled}
}

type observerTestTimer struct {
	ch      chan time.Time
	mu      sync.Mutex
	stopped bool
}

func (t *observerTestTimer) C() <-chan time.Time { return t.ch }

func (t *observerTestTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasRunning := !t.stopped
	t.stopped = true
	return wasRunning
}

type observerTestClock struct {
	timer *observerTestTimer
}

func (c *observerTestClock) NewTimer(time.Duration) sessiontrace.LivenessTimer {
	c.timer = &observerTestTimer{ch: make(chan time.Time, 1)}
	return c.timer
}

func TestObserverPublicLivenessBoundaryPublishesTypedFailureAndStops(t *testing.T) {
	clock := &observerTestClock{}
	observer := newObserverForTest(&observerTestSink{}, func(options *sessiontrace.NewObserverOptions) {
		options.LivenessClock = clock
	})
	errorsCh := observer.LivenessErrors(context.Background())
	observer.ArmProviderProgress()
	if clock.timer == nil {
		t.Fatal("ArmProviderProgress did not install a timer")
	}
	clock.timer.ch <- time.Now()
	select {
	case err := <-errorsCh:
		var typed *sessiontrace.LivenessError
		if !errors.As(err, &typed) {
			t.Fatalf("liveness error channel = %v, want typed timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("liveness error channel did not deliver")
	}
	var failure *sessiontrace.LivenessError
	if !errors.As(observer.LivenessFailure(), &failure) || failure.Classification != SessionSilentProviderTimeoutClassification {
		t.Fatalf("liveness failure = %v, want typed timeout", observer.LivenessFailure())
	}
	if observer.LivenessEvents() == nil {
		t.Fatal("liveness event channel became unavailable")
	}
	observer.StopLiveness()
	observer.ResetProviderProgress()
	observer.StopLiveness()
}

func TestObserverPublicEmptyResponseBoundaryLatchesAndReportsTypedFailure(t *testing.T) {
	sink := &observerTestSink{}
	observer := newObserverForTest(sink)
	var failure, terminal sessiontrace.TerminalObservation
	terminalCalls := 0
	failureCalls := 0
	observer.SetTerminalObserver(func(observation sessiontrace.TerminalObservation) bool {
		terminal = observation
		terminalCalls++
		return true
	})
	observer.SetFailureObserver(func(observation sessiontrace.TerminalObservation) {
		failure = observation
		failureCalls++
	})
	end := messages.NewMessageEndValue(messages.TokenUsage{})
	end.TerminalReason = messages.TerminalReasonPartialOutput
	end.OutputState = messages.TerminalOutputNone
	observer.ObserveSilentProviderEmptyResponse(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "empty-response",
		Value:      end,
	}, end, false, false)

	var liveness *sessiontrace.LivenessError
	if !errors.As(observer.LivenessFailure(), &liveness) || liveness.Classification != sessiontrace.SilentProviderEmptyResponseClassification || liveness.ResponseID != "empty-response" {
		t.Fatalf("empty-response liveness failure = %v, want a typed response-scoped failure", observer.LivenessFailure())
	}
	if err := observer.Finish(nil); !errors.As(err, &liveness) || !errors.Is(err, sessiontrace.ErrSilentProviderEmptyResponse) {
		t.Fatalf("Finish error = %v, want the latched public liveness cause", err)
	}
	if terminalCalls != 1 || failureCalls != 1 || !terminal.Failure || !failure.Failure || failure.Classification != sessiontrace.SilentProviderEmptyResponseClassification || failure.FailingEvent != string(messages.StreamTypeMessageEnd) {
		t.Fatalf("terminal/failure observations = %+v/%+v, calls=%d/%d", terminal, failure, terminalCalls, failureCalls)
	}
	if len(sink.recordsFor(sessiontrace.SessionDiagnosticEventFailure)) != 1 {
		t.Fatal("empty provider response did not produce one failure diagnostic")
	}
}

func TestObserverRoomBoundCancellationRemainsTerminalWithoutFailure(t *testing.T) {
	sink := &observerTestSink{}
	observer := newObserverForTest(sink)
	var terminal sessiontrace.TerminalObservation
	terminalCalls, failureCalls := 0, 0
	observer.SetTerminalObserver(func(got sessiontrace.TerminalObservation) bool {
		terminal = got
		terminalCalls++
		return true
	})
	observer.SetFailureObserver(func(sessiontrace.TerminalObservation) { failureCalls++ })
	observer.MarkRoomBoundCancellation()

	if err := observer.Finish(context.Canceled); err != nil {
		t.Fatalf("Finish(room-bound cancellation): %v", err)
	}
	if terminalCalls != 1 || terminal.Failure || !terminal.RoomBound || terminal.TerminalReason != messages.TerminalReasonCancellation || terminal.TerminalProvenance != messages.TerminalProvenanceRoom {
		t.Fatalf("terminal observation = %+v, calls=%d; want one room-bound cancellation", terminal, terminalCalls)
	}
	if failureCalls != 0 || len(sink.recordsFor(sessiontrace.SessionDiagnosticEventFailure)) != 0 {
		t.Fatalf("failure notifications/records = %d/%d, want none", failureCalls, len(sink.recordsFor(sessiontrace.SessionDiagnosticEventFailure)))
	}
}
