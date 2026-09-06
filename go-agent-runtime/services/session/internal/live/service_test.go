package live

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
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

// An explicit control may register its media barrier before an automatic
// provider send reaches the wrapper. The automatic send must be allowed to
// finish so the model runner can dispatch the control; waiting on the control
// barrier from the runner itself would deadlock both operations.
func TestOrderedSessionAutomaticSendAheadOfPendingControlDoesNotDeadlock(t *testing.T) {
	gate := mediagate.New(nil)
	ackID, _, err := gate.RegisterAck()
	if err != nil {
		t.Fatalf("register control: %v", err)
	}

	automaticStarted := make(chan struct{})
	releaseAutomatic := make(chan struct{})
	controlSent := make(chan struct{})
	provider := &orderingSession{
		automaticStarted: automaticStarted,
		releaseAutomatic: releaseAutomatic,
		controlSent:      controlSent,
	}
	ordered := &orderedSession{inner: provider, media: gate}

	automaticDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		automaticDone <- ordered.SendWithOutcome(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("automatic"),
		})
	}()
	select {
	case <-automaticStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic provider send did not start")
	}

	controlDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		controlDone <- ordered.SendWithOutcome(context.Background(), messages.StreamMessage{
			Type:            messages.StreamTypeMessageEnd,
			ActorProvidedID: ackID,
			Value:           messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	}()
	select {
	case <-controlSent:
		t.Fatal("control overtook the automatic send")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseAutomatic)

	select {
	case outcome := <-automaticDone:
		if !outcome.OK() {
			t.Fatalf("automatic send outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic provider send did not finish")
	}
	select {
	case outcome := <-controlDone:
		if !outcome.OK() {
			t.Fatalf("control send outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("control provider send deadlocked behind automatic send")
	}
	select {
	case <-controlSent:
	case <-time.After(time.Second):
		t.Fatal("control provider send did not run")
	}
}

type orderingSession struct {
	automaticStarted chan struct{}
	releaseAutomatic chan struct{}
	controlSent      chan struct{}
}

func (s *orderingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeMessageEnd {
		close(s.controlSent)
		return true
	}
	select {
	case <-s.automaticStarted:
	default:
		close(s.automaticStarted)
	}
	select {
	case <-s.releaseAutomatic:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *orderingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return messages.NewTypedBuffer[messages.StreamMessage](1)
}

func (s *orderingSession) Done() <-chan struct{} { return nil }

func (s *orderingSession) Close() error { return nil }
