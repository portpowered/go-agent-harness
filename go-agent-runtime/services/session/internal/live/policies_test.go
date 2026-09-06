package live

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestLiveFirstTurnTimeoutUsesInjectedScheduler(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(400, 0), time.Millisecond)
	provider := newTestSession()
	if !provider.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("provider-session", "audio_inference"),
	}) {
		t.Fatal("queue SESSION.OPEN")
	}
	service := New(Dependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return &testInferencer{session: provider}, nil
		},
		Clock: clock.Now, Scheduler: clock,
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "first-turn-policy", RequireFirstTurn: true, FirstTurnTimeout: 7 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for {
		event, ok := <-handle.Events()
		if !ok {
			t.Fatal("event stream closed before SESSION.OPEN")
		}
		if event.Kind == string(messages.StreamTypeSessionOpen) {
			break
		}
	}
	clock.AdvanceBy(7 * time.Millisecond)
	if err := handle.Wait(); !errors.Is(err, session.ErrLiveFirstTurnTimeout) {
		t.Fatalf("Wait = %v, want ErrLiveFirstTurnTimeout", err)
	}
}

func TestLiveRateLimitRetryUsesInjectedScheduler(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(500, 0), time.Millisecond)
	scheduler := &observingScheduler{Scheduler: clock, timerCreated: make(chan struct{})}
	provider := newTestSession()
	for _, message := range []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")},
		{Type: messages.StreamTypeMessageEnd, ResponseID: "response-1", Value: &messages.MessageEndValue{
			Type: "message_end", Status: "failed", ProviderErrorCode: "rate_limit_exceeded",
			ProviderErrorMessage: "Please try again in 0.005s",
		}},
	} {
		if !provider.receive.Write(context.Background(), message) {
			t.Fatal("queue provider message")
		}
	}
	service := New(Dependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return &testInferencer{session: provider}, nil
		},
		Clock: clock.Now, Scheduler: scheduler,
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "retry-policy", RateLimitRetry: session.LiveRateLimitRetryPolicy{
			Enabled: true, MaxRetries: 1, DefaultDelay: 5 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event, ok := <-handle.Events():
			if !ok {
				t.Fatal("event stream closed before rate-limit terminal")
			}
			if event.Kind == string(messages.StreamTypeMessageEnd) {
				goto rateLimitObserved
			}
		case <-deadline:
			t.Fatal("timed out waiting for rate-limit terminal")
		}
	}

rateLimitObserved:
	select {
	case <-scheduler.timerCreated:
	case <-time.After(time.Second):
		t.Fatal("rate-limit retry timer was not scheduled")
	}
	clock.AdvanceBy(5 * time.Millisecond)
	deadline = time.After(time.Second)
	for !provider.hasType(messages.StreamTypeResponseCreate) {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for retry response.create")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	stop := errors.New("stop retry fixture")
	handle.Cancel(stop)
	if err := handle.Wait(); !errors.Is(err, stop) {
		t.Fatalf("Wait = %v, want cancellation cause", err)
	}
}

func TestTimedToolExecutorUsesSchedulerDeadline(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(600, 0), time.Millisecond)
	tool := blockingTool{started: make(chan struct{})}
	executor := newTimedToolExecutor(tool, clock, 9*time.Millisecond)
	result := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-1", Name: "slow"})
		result <- err
	}()
	select {
	case <-tool.started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	clock.AdvanceBy(9 * time.Millisecond)
	select {
	case err := <-result:
		if !errors.Is(err, session.ErrLiveToolExecutionTimeout) {
			t.Fatalf("tool error = %v, want ErrLiveToolExecutionTimeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool deadline")
	}
}

type blockingTool struct{ started chan struct{} }

type observingScheduler struct {
	platformclock.Scheduler
	timerCreated chan struct{}
	once         sync.Once
}

func (scheduler *observingScheduler) NewTimer(duration time.Duration) platformclock.Timer {
	timer := scheduler.Scheduler.NewTimer(duration)
	scheduler.once.Do(func() { close(scheduler.timerCreated) })
	return timer
}

func (s *testSession) hasType(kind messages.StreamMessageType) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.sent {
		if message.Type == kind {
			return true
		}
	}
	return false
}

func (tool blockingTool) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if tool.started != nil {
		close(tool.started)
	}
	<-ctx.Done()
	return messages.ToolCallResponse{ToolCallID: call.ID}, ctx.Err()
}

func TestCapabilityAdmissionPreservesCleanupFailures(t *testing.T) {
	for _, phase := range []string{"initialize", "refresh", "closed"} {
		t.Run(phase, func(t *testing.T) {
			cause := errors.New("capability admission failed")
			closeErr := errors.New("capability cleanup failed")
			calls := 0
			binding := &session.LiveCapabilities{Close: func() error { calls++; return closeErr }}
			switch phase {
			case "initialize":
				binding.Initialize = func(context.Context) error { return cause }
			case "refresh":
				binding.RefreshDefinitions = func(context.Context) ([]messages.ToolDefinition, error) { return nil, cause }
			case "closed":
				cause = session.ErrLiveClosed
			}
			owner := &handle{request: session.LiveRequest{Capabilities: binding}, closed: phase == "closed"}
			_, _, err := owner.admitCapabilities(t.Context())
			if !errors.Is(err, cause) || !errors.Is(err, closeErr) || calls != 1 {
				t.Fatalf("error=%v, closes=%d; want both causes and exactly one close", err, calls)
			}
		})
	}
}

// This recorder observes the same source sequence as the public event stream,
// including observations that a slow presentation consumer cannot retain.
type eventSequenceRecorder struct{ events []session.LiveEvent }

func (r *eventSequenceRecorder) RecordMessage(context.Context, session.LiveRecord) error { return nil }
func (r *eventSequenceRecorder) RecordAudio(context.Context, session.LiveAudioRecord) error {
	return nil
}
func (r *eventSequenceRecorder) Finalize(context.Context, error) error { return nil }
func (r *eventSequenceRecorder) RecordEvent(_ context.Context, event session.LiveEvent) error {
	r.events = append(r.events, event)
	return nil
}

func TestLiveEvidenceAndPresentationShareSequenceIncludingOverflow(t *testing.T) {
	recorder := &eventSequenceRecorder{}
	h := &handle{events: make(chan session.LiveEvent, 4), parentCtx: t.Context(), clock: func() time.Time { return time.Unix(700, 0) }}
	h.setRecorder(recorder)
	for index := 0; index < 6; index++ {
		h.publish(session.LiveEvent{Kind: string(session.LiveEventText)}, false)
	}
	h.publish(session.LiveEvent{Kind: string(session.LiveEventTerminal)}, true)
	if len(recorder.events) != 8 {
		t.Fatalf("recorded %d observations, want six text, overflow, terminal", len(recorder.events))
	}
	for index, event := range recorder.events {
		if event.Sequence != uint64(index+1) || event.Timestamp.IsZero() {
			t.Fatalf("evidence identity at %d: %+v", index, event)
		}
	}
	var overflow uint64
	for event := range h.events {
		recorded := recorder.events[event.Sequence-1]
		if event.Kind != recorded.Kind || event.Dropped != recorded.Dropped {
			t.Fatalf("presentation/evidence mismatch: %+v / %+v", event, recorded)
		}
		if event.Kind == string(session.LiveEventOverflow) {
			overflow = event.Dropped
		}
	}
	if overflow != 4 {
		t.Fatalf("overflow = %d, want three rejected plus one evicted", overflow)
	}
}

func TestFinalizationFailureCannotRetainSuccessfulTerminalClassification(t *testing.T) {
	observed := messages.NewSessionCloseValueWithTerminal("fixture", "finished", "completed", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	cause := errors.New("provider capture could not be flushed")
	terminal := finalizeLiveTerminalValue(session.LiveRequest{SessionID: "fixture"}, cause, observed, nil)
	if terminal.TerminalReason != messages.TerminalReasonTerminalFailure || terminal.TerminalProvenance != messages.TerminalProvenanceSession {
		t.Fatalf("finalization failure reported as successful completion: %+v", terminal)
	}
	if observed.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion {
		t.Fatal("original provider observation mutated")
	}
}

type terminalFailureRecorder struct {
	eventSequenceRecorder
	cause error
}

func (r *terminalFailureRecorder) RecordEvent(context.Context, session.LiveEvent) error {
	return r.cause
}

func TestTerminalRecorderFailureChangesWaitCauseAndDeliveredTerminal(t *testing.T) {
	cause := errors.New("terminal evidence write failed")
	h := &handle{events: make(chan session.LiveEvent, 4), parentCtx: t.Context()}
	h.setRecorder(&terminalFailureRecorder{cause: cause})
	value := messages.NewSessionCloseValueWithTerminal("fixture", "finished", "completed", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	h.publish(session.LiveEvent{Kind: string(session.LiveEventTerminal), Terminal: value}, true)
	event := <-h.events
	if !errors.Is(event.Error, cause) || !errors.Is(h.terminalErr, cause) {
		t.Fatalf("terminal recording error lost: event=%v wait=%v", event.Error, h.terminalErr)
	}
	if event.Terminal.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("failed terminal recorded as success: %+v", event.Terminal)
	}
}

func TestLiveRecorderFailureBecomesInvocationCause(t *testing.T) {
	provider := newTestSession()
	if !provider.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("provider-session", "audio_inference"),
	}) {
		t.Fatal("queue SESSION.OPEN")
	}
	if !provider.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("provider-session", "complete"),
	}) {
		t.Fatal("queue SESSION.CLOSE")
	}
	recordingErr := errors.New("recording sink failed")
	recorder := &failingLiveRecorder{
		messageErr: recordingErr,
		finalized:  make(chan struct{}),
		recorded:   make(chan struct{}),
	}
	go func() {
		<-recorder.recorded
		if err := provider.Close(); err != nil {
			t.Errorf("close provider: %v", err)
		}
	}()
	service := New(Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: provider}, nil
	}})
	err := service.RunLive(context.Background(), session.LiveRunOptions{
		Request:  session.LiveRequest{SessionID: "recording-failure"},
		Recorder: recorder,
	})
	if !errors.Is(err, recordingErr) {
		t.Fatalf("RunLive error = %v, want recording cause", err)
	}
	if recorder.contextErr != nil {
		t.Fatalf("recorder received canceled context: %v", recorder.contextErr)
	}
	select {
	case <-recorder.finalized:
	case <-time.After(time.Second):
		t.Fatal("recorder was not finalized")
	}
}

// A provider may signal application completion before its transport closes.
// The live owner must initiate cleanup rather than waiting for peer EOF first.
func TestLiveSessionCloseInitiatesProviderCleanup(t *testing.T) {
	provider := newTestSession()
	terminal := messages.NewSessionCloseValue("provider-session", "complete")
	for _, event := range []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")},
		{Type: messages.StreamTypeSessionClose, Value: terminal},
	} {
		if !provider.receive.Write(t.Context(), event) {
			t.Fatal("queue provider lifecycle")
		}
	}
	service := New(Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: provider}, nil
	}})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	handle, err := service.OpenLive(ctx, session.LiveRequest{SessionID: "in-band-close"})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(); err != nil {
		t.Fatalf("Wait after SESSION.CLOSE: %v", err)
	}
	select {
	case <-provider.Done():
	default:
		t.Fatal("Wait returned before provider cleanup completed")
	}
	events := collectTestLiveEvents(handle.Events())
	var closed bool
	for _, event := range events {
		if event.Kind == string(messages.StreamTypeSessionClose) && event.Reason == "complete" {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("provider terminal boundary was lost: %+v", events)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close after Wait: %v", err)
	}
}

func TestLateCaptureCompletionRespectsOutstandingResponseWork(t *testing.T) {
	for _, test := range []struct {
		name         string
		change       func(*handle)
		wantFinished bool
	}{
		{name: "response already complete", wantFinished: true},
		{name: "response active", change: func(h *handle) { h.responseActive = true }},
		{name: "commit pending", change: func(h *handle) { h.responsePending = true }},
		{name: "tool pending", change: func(h *handle) { h.pendingToolCalls = 1 }},
		{name: "next response required", change: func(h *handle) { h.request.ExpectedResponses = 2 }},
		{name: "wait for provider close", change: func(h *handle) { h.request.ReplayPlan = &session.LiveReplayPlan{ProviderCloseExpected: true} }},
		{name: "persistent policy", change: func(h *handle) { h.request.FinishAfterResponse = false }},
		{name: "cancellation", change: func(h *handle) { h.cancelRequested = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &handle{request: session.LiveRequest{FinishAfterResponse: true}, responseStarted: true, replayResponses: 1}
			if test.change != nil {
				test.change(h)
			}
			h.markCaptureComplete()
			if !h.captureComplete || h.gracefulStop != test.wantFinished {
				t.Fatalf("EOF completion = captured:%t finished:%t, want captured:true finished:%t", h.captureComplete, h.gracefulStop, test.wantFinished)
			}
		})
	}
}
