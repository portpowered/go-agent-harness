package seed

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

type seedTestSession struct {
	mu       sync.Mutex
	sent     []messages.StreamMessage
	incoming *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	close    sync.Once
	outcome  messages.SessionSendOutcome
	terminal error
}

func newSeedTestSession() *seedTestSession {
	return &seedTestSession{
		incoming: messages.NewTypedBuffer[messages.StreamMessage](8),
		done:     make(chan struct{}),
		outcome:  messages.SessionSendOutcome{Status: messages.SessionSendSucceeded},
	}
}

func (s *seedTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *seedTestSession) SendWithOutcome(_ context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	outcome := s.outcome
	s.mu.Unlock()
	return outcome
}

func (s *seedTestSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *seedTestSession) SupportsResponseRequests() bool { return true }

func (s *seedTestSession) SendMessage(context.Context, messages.Message) bool { return true }

func (s *seedTestSession) SendMessageWithoutResponse(context.Context, messages.Message) bool {
	return true
}

func (s *seedTestSession) SupportsCompleteMessages() bool { return true }

func (s *seedTestSession) SupportsCompleteMessagesWithoutResponse() bool { return true }

func (s *seedTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.incoming }

func (s *seedTestSession) Done() <-chan struct{} { return s.done }

func (s *seedTestSession) TerminalError() error { return s.terminal }

func (s *seedTestSession) Close() error {
	s.close.Do(func() { close(s.done) })
	return nil
}

type seedTestInferencer struct {
	session messages.Session
	request inference.SessionRequest
}

func (i seedTestInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func (i seedTestInferencer) Request() inference.SessionRequest { return i.request }

func TestServiceWrapsSeedAndForwardsCapabilities(t *testing.T) {
	inner := newSeedTestSession()
	inner.terminal = errors.New("provider stopped")
	service := New(sessionturn.AllocatorFunc(func() string { return "allocated" }))
	wrapped := service.WrapSession(context.Background(), inner, "wire-prompt", sessionturn.Seed{Value: "seed", Present: true})
	t.Cleanup(func() {
		if err := wrapped.Close(); err != nil {
			t.Errorf("cleanup close: %v", err)
		}
	})

	first := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("wire-prompt")}
	if !wrapped.Send(context.Background(), first) {
		t.Fatal("seed message was rejected")
	}
	second := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("wire-prompt")}
	if !wrapped.Send(context.Background(), second) {
		t.Fatal("second message was rejected")
	}
	inner.mu.Lock()
	firstValue, firstOK := inner.sent[0].Value.(*messages.TextDeltaValue)
	secondValue, secondOK := inner.sent[1].Value.(*messages.TextDeltaValue)
	inner.mu.Unlock()
	if !firstOK || firstValue.Content != "seed" {
		t.Fatalf("seed content = %#v", firstValue)
	}
	if !secondOK || secondValue.Content != "wire-prompt" {
		t.Fatalf("second content = %#v", secondValue)
	}

	if !wrapped.SupportsResponseRequests() || !wrapped.RequestResponse(context.Background()).OK() {
		t.Fatal("response capability was not forwarded")
	}
	if !wrapped.SupportsCompleteMessages() || !wrapped.SendMessage(context.Background(), messages.Message{}) {
		t.Fatal("complete-message capability was not forwarded")
	}
	if !wrapped.SupportsCompleteMessagesWithoutResponse() || !wrapped.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("queued complete-message capability was not forwarded")
	}
	if !errors.Is(wrapped.TerminalError(), inner.terminal) {
		t.Fatal("terminal error identity was not forwarded")
	}

	incoming := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("inbound")}
	if !inner.incoming.Write(context.Background(), incoming) {
		t.Fatal("inbound message was rejected")
	}
	got, ok := wrapped.Receive().ReadBlocking(wrapped.Done())
	gotValue, valueOK := got.Value.(*messages.TextDeltaValue)
	if !ok || !valueOK || gotValue.Content != "inbound" {
		t.Fatalf("inbound message = %#v, ok=%v", got, ok)
	}
	if got := service.Allocate(); got != "allocated" {
		t.Fatalf("allocated value = %q", got)
	}
}

func TestServicePreservesOmittedSeedAndInferencerRequest(t *testing.T) {
	inner := newSeedTestSession()
	service := New(nil)
	wrapped := service.WrapSession(context.Background(), inner, "wire-prompt", sessionturn.Seed{})
	if !wrapped.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("wire-prompt"),
	}) {
		t.Fatal("omitted seed message was rejected")
	}
	inner.mu.Lock()
	value, valueOK := inner.sent[0].Value.(*messages.TextDeltaValue)
	inner.mu.Unlock()
	if !valueOK || value.Content != "wire-prompt" {
		t.Fatalf("omitted seed content = %#v", value)
	}

	inferencer := service.WrapInferencer(seedTestInferencer{session: inner}, "wire-prompt", sessionturn.Seed{})
	requester, ok := inferencer.(interface {
		Request() inference.SessionRequest
	})
	if !ok {
		t.Fatal("wrapped inferencer did not expose its request")
	}
	if got := requester.Request(); !reflect.DeepEqual(got, inference.SessionRequest{}) {
		t.Fatalf("inferencer request = %#v", got)
	}
	connected, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect session: %v", err)
	}
	if err := connected.Close(); err != nil {
		t.Fatalf("close connected session: %v", err)
	}

	if got := New(nil).Allocate(); got == "" {
		t.Fatal("default allocator returned an empty value")
	}
	sink := New(nil).NewOutput(nil)
	if n, err := sink.Write([]byte("data")); n != 0 || err == nil {
		t.Fatalf("nil output write = (%d, %v)", n, err)
	}
	if sink.Err() == nil {
		t.Fatal("nil output error was not retained")
	}
	short := output{writer: shortWriter{}}
	if n, err := short.Write([]byte("data")); n != 3 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short output write = (%d, %v)", n, err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestServiceCancellationClosesInnerSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := newSeedTestSession()
	wrapped := New(nil).WrapSession(ctx, inner, "wire-prompt", sessionturn.Seed{})
	cancel()
	select {
	case <-inner.Done():
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close the inner session")
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

type basicSeedSession struct {
	incoming *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	close    sync.Once
}

func newBasicSeedSession() *basicSeedSession {
	return &basicSeedSession{
		incoming: messages.NewTypedBuffer[messages.StreamMessage](1),
		done:     make(chan struct{}),
	}
}

func (s *basicSeedSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *basicSeedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.incoming }

func (s *basicSeedSession) Done() <-chan struct{} { return s.done }

func (s *basicSeedSession) Close() error {
	s.close.Do(func() { close(s.done) })
	return nil
}

func TestServiceReportsAbsentOptionalCapabilities(t *testing.T) {
	inner := newBasicSeedSession()
	wrapped := New(nil).WrapSession(context.Background(), inner, "wire-prompt", sessionturn.Seed{})
	if wrapped.SupportsResponseRequests() || wrapped.SupportsCompleteMessages() || wrapped.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("wrapper reported an absent optional capability")
	}
	if wrapped.SendMessage(context.Background(), messages.Message{}) || wrapped.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("wrapper accepted an absent complete-message capability")
	}
	if wrapped.TerminalError() != nil {
		t.Fatal("wrapper reported an absent terminal error")
	}
	mediaOwner, ok := wrapped.(interface {
		RTCMedia() (sharedaudio.MediaEndpoints, bool)
	})
	if !ok {
		t.Fatal("wrapped session did not expose media capability probe")
	}
	if _, ok := mediaOwner.RTCMedia(); ok {
		t.Fatal("wrapper reported absent media capability")
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestWrappedSessionReportsToolResultAndContinuationOutcomes(t *testing.T) {
	lifecycle := sessiontracewire.NewLifecycleService()
	ctx := context.Background()
	if _, err := lifecycle.Apply(ctx, sessiontrace.LifecycleEvent{Kind: sessiontrace.LifecycleEventResponseOpen, ResponseID: "response-1"}); err != nil {
		t.Fatalf("open response: %v", err)
	}
	for _, callID := range []string{"complete", "rejected", "continued"} {
		if _, err := lifecycle.Apply(ctx, sessiontrace.LifecycleEvent{Kind: sessiontrace.LifecycleEventToolCall, ResponseID: "response-1", CallID: callID, ToolName: "lookup"}); err != nil {
			t.Fatalf("record tool call %q: %v", callID, err)
		}
	}

	var observed []sessionturn.ToolLifecycleEvent
	inner := newSeedTestSession()
	wrapped := New(nil, Options{Lifecycle: lifecycle, Observer: func(event sessionturn.ToolLifecycleEvent) {
		observed = append(observed, event)
	}}).WrapSession(ctx, inner, "", sessionturn.Seed{})
	if !wrapped.SendMessage(ctx, messages.Message{ToolCallID: "complete"}) {
		t.Fatal("provider rejected the complete tool result")
	}
	inner.mu.Lock()
	inner.outcome = messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	inner.mu.Unlock()
	rejected := wrapped.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("rejected", "lookup", "{}"),
	})
	if rejected.Status != messages.SessionSendClosed {
		t.Fatalf("rejected tool result outcome = %+v, want closed status", rejected)
	}
	inner.mu.Lock()
	inner.outcome = messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	inner.mu.Unlock()
	if !wrapped.SendMessage(ctx, messages.Message{ToolCallID: "continued"}) {
		t.Fatal("provider rejected the tool result that should request continuation")
	}

	states := lifecycle.Snapshot().ContinuationStates
	byID := make(map[string]sessiontrace.LifecycleContinuationState, len(states))
	for _, state := range states {
		byID[state.CallID] = state
	}
	if !byID["complete"].ResultAccepted || !byID["complete"].ToolResponseComplete {
		t.Fatalf("complete result state = %+v, want accepted and complete", byID["complete"])
	}
	if !byID["rejected"].ResultRejected || byID["rejected"].ResultRejectionStatus != string(messages.SessionSendClosed) {
		t.Fatalf("rejected result state = %+v, want closed rejection", byID["rejected"])
	}
	if !byID["continued"].ResultAccepted || !byID["continued"].ContinuationRequested {
		t.Fatalf("continuing result state = %+v, want accepted continuation", byID["continued"])
	}
	seen := make(map[sessionturn.ToolLifecycleEventType]map[string]sessionturn.ToolLifecycleEvent)
	for _, event := range observed {
		if seen[event.Type] == nil {
			seen[event.Type] = make(map[string]sessionturn.ToolLifecycleEvent)
		}
		seen[event.Type][event.CallID] = event
	}
	if len(observed) != 5 || seen[sessionturn.ToolResultAccepted]["complete"].CallID != "complete" || seen[sessionturn.ToolResultRejected]["rejected"].Status != messages.SessionSendClosed || seen[sessionturn.ToolResultAccepted]["continued"].CallID != "continued" || seen[sessionturn.ToolContinuationRequested]["complete"].CallID != "complete" || seen[sessionturn.ToolContinuationRequested]["continued"].CallID != "continued" {
		t.Fatalf("tool lifecycle observations = %+v, want accepted/rejected results and per-call continuations", observed)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("close wrapped session: %v", err)
	}
}
