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

type testInferencer struct {
	session *testSession
}

func (i *testInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type testSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	close   sync.Once
	mu      sync.Mutex
	sent    []messages.StreamMessage
}

type failingLiveRecorder struct {
	messageErr  error
	finalized   chan struct{}
	recorded    chan struct{}
	recordOnce  sync.Once
	contextErr  error
	finalizeErr error
}

func (r *failingLiveRecorder) RecordMessage(ctx context.Context, _ session.LiveRecord) error {
	if ctx != nil {
		r.contextErr = ctx.Err()
	}
	if r.recorded != nil {
		r.recordOnce.Do(func() { close(r.recorded) })
	}
	return r.messageErr
}

func (*failingLiveRecorder) RecordAudio(_ context.Context, _ session.LiveAudioRecord) error {
	return nil
}

func (*failingLiveRecorder) RecordEvent(_ context.Context, _ session.LiveEvent) error { return nil }

func (r *failingLiveRecorder) Finalize(context.Context, error) error {
	if r.finalized != nil {
		close(r.finalized)
	}
	return r.finalizeErr
}

type testLiveCapabilityHandle struct {
	initialized chan struct{}
	closed      chan struct{}
	events      chan session.LiveCapabilityEvent
}

func (h *testLiveCapabilityHandle) Initialize(context.Context) error {
	select {
	case <-h.initialized:
	default:
		close(h.initialized)
	}
	return nil
}

func (h *testLiveCapabilityHandle) RefreshDefinitions(context.Context) ([]messages.ToolDefinition, error) {
	return nil, nil
}

func (h *testLiveCapabilityHandle) BrowserWatch(context.Context) <-chan session.LiveCapabilityEvent {
	return h.events
}

func (h *testLiveCapabilityHandle) Close() error {
	select {
	case <-h.closed:
	default:
		close(h.closed)
	}
	return nil
}

func newTestSession() *testSession {
	return &testSession{receive: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{})}
}

func (s *testSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *testSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *testSession) Done() <-chan struct{}                                  { return s.done }
func (s *testSession) Close() error {
	s.close.Do(func() { close(s.done) })
	return nil
}

func (s *testSession) hasText(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.sent {
		if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value.Content == text {
			return true
		}
	}
	return false
}

func TestOpenLiveIsInertUntilStart(t *testing.T) {
	s := newTestSession()
	called := make(chan session.LiveRequest, 1)
	service := New(Dependencies{InferencerFactory: func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		called <- request
		return &testInferencer{session: s}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{SessionID: "inert", OpeningPrompt: "hello"})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	select {
	case <-called:
		t.Fatal("provider factory called during OpenLive")
	case <-time.After(20 * time.Millisecond):
	}
	if err := handle.Wait(); !errors.Is(err, session.ErrLiveNotStarted) {
		t.Fatalf("Wait before Start = %v, want ErrLiveNotStarted", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close before Start: %v", err)
	}
}

func TestLiveStartSendsOpeningPromptAndPreservesCancelCause(t *testing.T) {
	s := newTestSession()
	s.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("provider-session", "audio_inference"),
	})
	service := New(Dependencies{InferencerFactory: func(_ context.Context, _ session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: s}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{SessionID: "request-session", OpeningPrompt: "opening question"})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	//lint:ignore SA1012 Verify invalid admission leaves the handle available for a valid Start.
	if err := handle.Start(nil); err == nil {
		t.Fatal("nil Start context accepted")
	}
	if err := handle.Wait(); !errors.Is(err, session.ErrLiveNotStarted) {
		t.Fatalf("rejected Start changed lifecycle: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := awaitSessionOpen(t, handle.Events())
	waitForSentText(t, s, "opening question")
	wantErr := errors.New("room stopped")
	handle.Cancel(wantErr)
	if err := handle.Wait(); !errors.Is(err, wantErr) {
		t.Fatalf("Wait = %v, want cancellation cause", err)
	}
	got = append(got, collectTestLiveEvents(handle.Events())...)
	if len(got) == 0 || got[len(got)-1].Kind != string(session.LiveEventTerminal) {
		t.Fatalf("last event = %#v, want terminal", got)
	}
}

func TestLiveCancelPreservesFirstCauseAcrossTeardown(t *testing.T) {
	service := New(Dependencies{InferencerFactory: func(_ context.Context, _ session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: newTestSession()}, nil
	}})
	opened, err := service.OpenLive(context.Background(), session.LiveRequest{SessionID: "first-cause"})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := opened.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h, ok := opened.(*handle)
	if !ok {
		t.Fatalf("handle type = %T, want *handle", opened)
	}
	cause := errors.New("provider liveness failure")
	h.Cancel(cause)
	// A later transport/media teardown must not replace the actionable cause
	// with context.Canceled while the invocation is joining.
	h.Cancel(context.Canceled)
	if waitErr := h.Wait(); !errors.Is(waitErr, cause) {
		t.Fatalf("Wait = %v, want first cause %v", waitErr, cause)
	}
}

func TestUserCancellationWinsOverUnresolvedToolResultTeardown(t *testing.T) {
	state := finishState{
		requestedErr:  context.Canceled,
		parentCause:   session.ErrLiveUserCancellation,
		toolResultErr: &session.LiveUnresolvedToolResultsError{CallIDs: []string{"call-in-flight"}},
	}
	if !state.userCancellation() {
		t.Fatal("explicit user cancellation was overridden by the expected unresolved-tool teardown diagnostic")
	}
}

func TestLiveCapabilityHandleOwnsLifecycleAndBrowserEvents(t *testing.T) {
	provider := newTestSession()
	capability := &testLiveCapabilityHandle{
		initialized: make(chan struct{}),
		closed:      make(chan struct{}),
		events:      make(chan session.LiveCapabilityEvent, 1),
	}
	service := New(Dependencies{InferencerFactory: func(_ context.Context, _ session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: provider}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID:     "session-with-browser",
		ParticipantID: "participant-a",
		Capabilities:  &session.LiveCapabilities{Handle: capability},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-capability.initialized:
	case <-time.After(time.Second):
		t.Fatal("capability handle was not initialized")
	}
	capability.events <- session.LiveCapabilityEvent{
		Type: "invocation_completed", Sequence: 17, BrowserID: "browser-a", TargetID: "tab-a",
		Generation: 3, InvocationID: "inv-1", ToolName: "read_page", State: "completed",
	}
	var observed session.LiveEvent
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-handle.Events():
			if event.Capability != nil {
				observed = event
				goto observedEvent
			}
		case <-deadline:
			t.Fatal("timed out waiting for browser capability event")
		}
	}

observedEvent:
	if observed.Kind != "browser.invocation_completed" || observed.BrowserID != "browser-a" || observed.InvocationID != "inv-1" {
		t.Fatalf("browser event projection = %+v", observed)
	}
	if observed.Capability.Sequence != 17 || observed.Capability.ToolName != "read_page" {
		t.Fatalf("typed browser event projection = %+v", observed.Capability)
	}
	cause := errors.New("stop browser session")
	handle.Cancel(cause)
	if err := handle.Wait(); !errors.Is(err, cause) {
		t.Fatalf("Wait = %v, want cancellation cause", err)
	}
	select {
	case <-capability.closed:
	case <-time.After(time.Second):
		t.Fatal("capability handle was not closed")
	}
}

func TestLiveMaxDurationUsesInjectedScheduler(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(100, 0), time.Millisecond)
	service := New(Dependencies{
		InferencerFactory: func(_ context.Context, _ session.LiveRequest) (messages.SessionInferencer, error) {
			return &testInferencer{session: newTestSession()}, nil
		},
		Clock:     clock.Now,
		Scheduler: clock,
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "duration-policy", MaxDuration: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	clock.AdvanceBy(25 * time.Millisecond)
	wait := make(chan error, 1)
	go func() { wait <- handle.Wait() }()
	select {
	case err := <-wait:
		if !errors.Is(err, session.ErrLiveDurationExceeded) {
			t.Fatalf("Wait = %v, want ErrLiveDurationExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduled duration cancellation")
	}
}

func TestLiveSessionUpdatedWatchdogUsesInjectedScheduler(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(200, 0), time.Millisecond)
	provider := newTestSession()
	if !provider.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("provider-session", "audio_inference"),
	}) {
		t.Fatal("queue SESSION.OPEN")
	}
	service := New(Dependencies{
		InferencerFactory: func(_ context.Context, _ session.LiveRequest) (messages.SessionInferencer, error) {
			return &testInferencer{session: provider}, nil
		},
		Clock:     clock.Now,
		Scheduler: clock,
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "session-updated-policy", RequireSessionUpdated: true,
		SessionUpdatedTimeout: 15 * time.Millisecond,
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
			t.Fatal("live event stream closed before SESSION.OPEN")
		}
		if event.Kind == string(messages.StreamTypeSessionOpen) {
			break
		}
	}
	clock.AdvanceBy(15 * time.Millisecond)
	wait := make(chan error, 1)
	go func() { wait <- handle.Wait() }()
	select {
	case err := <-wait:
		if !errors.Is(err, session.ErrLiveSessionUpdatedTimeout) {
			t.Fatalf("Wait = %v, want ErrLiveSessionUpdatedTimeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SESSION.UPDATED watchdog")
	}
}

func TestLiveTimingPolicyRequiresScheduler(t *testing.T) {
	service := New(Dependencies{InferencerFactory: func(_ context.Context, _ session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: newTestSession()}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{MaxDuration: time.Second})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); !errors.Is(err, session.ErrLiveSchedulerUnavailable) {
		t.Fatalf("Start = %v, want ErrLiveSchedulerUnavailable", err)
	}
}

func TestProviderLivenessEmptyResponsePublishesFaultBeforeTerminal(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(700, 0), time.Millisecond)
	provider := newTestSession()
	queueProviderMessages(t, provider, []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-empty", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-empty", Value: messages.NewMessageEndValueWithTerminal(
			messages.TokenUsage{}, messages.TerminalReasonPartialOutput, messages.TerminalProvenanceProvider, messages.TerminalOutputNone,
		)},
	})
	service := New(Dependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return &testInferencer{session: provider}, nil
		},
		Clock: clock.Now, Scheduler: clock,
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "liveness-empty", ProviderLiveness: session.LiveLivenessPolicy{Enabled: true, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitErr := handle.Wait()
	if !errors.Is(waitErr, session.ErrLiveSilentProviderEmptyResponse) {
		t.Fatalf("Wait = %v, want empty-response liveness error", waitErr)
	}
	assertEmptyResponseEvents(t, collectTestLiveEvents(handle.Events()))
}

func TestProviderLivenessTimeoutUsesInjectedScheduler(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(800, 0), time.Millisecond)
	provider := newTestSession()
	service := New(Dependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return &testInferencer{session: provider}, nil
		},
		Clock: clock.Now, Scheduler: clock,
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "liveness-timeout", ProviderLiveness: session.LiveLivenessPolicy{Enabled: true, Timeout: 9 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := handle.Send(context.Background(), session.LiveControl{Kind: session.LiveControlResponseCreate}); err != nil {
		t.Fatalf("Send response.create: %v", err)
	}
	clock.AdvanceBy(9 * time.Millisecond)
	waitErr := handle.Wait()
	if !errors.Is(waitErr, session.ErrLiveSilentProviderTimeout) {
		t.Fatalf("Wait = %v, want timeout liveness error", waitErr)
	}
	var fault, terminal *session.LiveEvent
	for event := range handle.Events() {
		copy := event
		if event.Kind == string(session.LiveEventLiveness) {
			fault = &copy
		}
		if event.Kind == string(session.LiveEventTerminal) {
			terminal = &copy
		}
	}
	if fault == nil || fault.Liveness == nil || fault.Liveness.Classification != "silent_provider_timeout" {
		t.Fatalf("timeout liveness event = %+v", fault)
	}
	if terminal == nil || terminal.Terminal == nil || terminal.Terminal.Classification != "silent_provider_timeout" {
		t.Fatalf("timeout terminal event = %+v", terminal)
	}
}

func TestLiveEventsRemainBoundedAndTerminalIsRetained(t *testing.T) {
	s := newTestSession()
	for i := 0; i < 500; i++ {
		s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("x")})
	}
	const participantID = "bounded-participant"
	eventAt := time.Unix(300, 0)
	service := New(Dependencies{EventCapacity: 4, Clock: func() time.Time { return eventAt }, InferencerFactory: func(_ context.Context, _ session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: s}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{SessionID: "bounded", ParticipantID: participantID})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	handle.Cancel(errors.New("stop bounded fixture"))
	if err := handle.Wait(); err != nil {
		if !errors.Is(err, context.Canceled) && err.Error() != "stop bounded fixture" {
			t.Fatalf("Wait: %v", err)
		}
	}
	if len(handle.Events()) > 4 {
		t.Fatalf("event queue length = %d, capacity = 4", len(handle.Events()))
	}
	foundTerminal := false
	foundOverflow := false
	for event := range handle.Events() {
		if event.Kind == string(session.LiveEventTerminal) {
			foundTerminal = true
		}
		if event.Kind == string(session.LiveEventOverflow) {
			foundOverflow = true
			if event.ParticipantID != participantID || !event.Timestamp.Equal(eventAt) {
				t.Fatalf("overflow metadata = %+v, want participant/timestamp preserved", event)
			}
		}
	}
	if !foundTerminal {
		t.Fatal("terminal event was lost")
	}
	if !foundOverflow {
		t.Fatal("overflow evidence was lost")
	}
}

func TestCaptureCompletionWaitsForResponseAfterContinuousEOF(t *testing.T) {
	h := &handle{
		request:             session.LiveRequest{FinishAfterResponse: true},
		captureSourceActive: true,
		responseStarted:     true,
		replayResponses:     1,
	}

	h.markCaptureComplete()
	if h.gracefulStop {
		t.Fatal("continuous EOF reused the response completed before capture ended")
	}
	if got, want := h.captureResponseTarget, 2; got != want {
		t.Fatalf("capture response target = %d, want %d", got, want)
	}

	h.replayResponses++
	h.markCaptureComplete()
	if !h.gracefulStop {
		t.Fatal("post-EOF response did not complete the finite capture")
	}
}

func TestFailedToolContinuationWinsAcrossToolResultObservationOrder(t *testing.T) {
	cases := []failedContinuationOrder{
		{name: "provider_failure_first", failureBeforeToolEnd: true},
		{name: "tool_result_first"},
		{name: "tool_output_first", outputBeforeAdmission: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertFailedContinuationOrder(t, tc) })
	}
}

type failedContinuationOrder struct {
	name                  string
	outputBeforeAdmission bool
	failureBeforeToolEnd  bool
}

func assertFailedContinuationOrder(t *testing.T, order failedContinuationOrder) {
	t.Helper()
	const callID = "call-failed-continuation"
	h := &handle{toolContinuations: make(map[string]*liveToolContinuation)}
	h.observeProviderToolCall(messages.StreamMessage{
		Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant,
		ToolCallId: callID, Value: messages.NewToolCallEndValue(callID, "read_image", `{}`),
	})
	toolOutput := messages.StreamMessage{
		Type: messages.StreamTypeImageEnd, Role: messages.RoleTool, ToolCallId: callID,
		Value: messages.NewImageEndValue(),
	}
	if order.outputBeforeAdmission {
		assertContinuationPending(t, h, toolOutput)
	}
	h.observeToolResult(callID, "read_image", true)
	if !order.outputBeforeAdmission {
		assertContinuationPending(t, h, toolOutput)
	}
	toolEnd := messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	failure := messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant,
		Value: &messages.MessageEndValue{
			Type: "message_end", Status: continuationStatusFailed, ProviderErrorCode: "token_limit_exceeded",
		},
	}
	var err error
	var complete bool
	if order.failureBeforeToolEnd {
		if earlyErr, earlyComplete := h.observeToolLifecycle(failure); earlyErr != nil || earlyComplete {
			t.Fatalf("early provider failure = error:%v complete:%t, want deferred classification", earlyErr, earlyComplete)
		}
		err, complete = h.observeToolLifecycle(toolEnd)
	} else {
		assertContinuationPending(t, h, toolEnd)
		err, complete = h.observeToolLifecycle(failure)
	}
	if !errors.Is(err, session.ErrLiveImageContinuationIncomplete) || complete {
		t.Fatalf("failed continuation = error:%v complete:%t, want typed failure", err, complete)
	}
}

func assertContinuationPending(t *testing.T, h *handle, msg messages.StreamMessage) {
	t.Helper()
	if err, complete := h.observeToolLifecycle(msg); err != nil || complete {
		t.Fatalf("continuation intermediate event = error:%v complete:%t, want pending", err, complete)
	}
}
