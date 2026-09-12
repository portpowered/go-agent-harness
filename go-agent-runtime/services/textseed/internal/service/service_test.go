package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
)

type fakeSession struct {
	mu            sync.Mutex
	sent          []messages.StreamMessage
	complete      []messages.Message
	queued        []messages.Message
	responseCalls int
	incoming      *messages.TypedBuffer[messages.StreamMessage]
	done          chan struct{}
	closeOnce     sync.Once
	outcome       messages.SessionSendOutcome
	response      bool
	terminal      error
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		incoming: messages.NewTypedBuffer[messages.StreamMessage](512),
		done:     make(chan struct{}),
		outcome:  messages.SessionSendOutcome{Status: messages.SessionSendSucceeded},
	}
}

func (s *fakeSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *fakeSession) SendWithOutcome(_ context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	outcome := s.outcome
	s.mu.Unlock()
	return outcome
}

func (s *fakeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.incoming }

func (s *fakeSession) Done() <-chan struct{} { return s.done }

func (s *fakeSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *fakeSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	s.mu.Lock()
	s.responseCalls++
	s.mu.Unlock()
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *fakeSession) SupportsResponseRequests() bool { return s.response }

func (s *fakeSession) SendMessage(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.complete = append(s.complete, msg)
	return true
}

func (s *fakeSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = append(s.queued, msg)
	return true
}

func (s *fakeSession) TerminalError() error { return s.terminal }

type fakeInferencer struct{ session messages.Session }

func (i fakeInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func newTestService(t *testing.T) textseed.Service {
	t.Helper()
	return New(textseed.AllocatorFunc(func() string { return "\x00textseed:test:1" }))
}

func TestServiceAllocatesNonEmptyUniqueValuesConcurrently(t *testing.T) {
	service := NewDefault()
	const count = 512
	values := make(chan string, count)
	var group sync.WaitGroup
	for range count {
		group.Go(func() { values <- service.Allocate() })
	}
	group.Wait()
	close(values)

	seen := make(map[string]struct{}, count)
	for value := range values {
		if value == "" {
			t.Fatal("allocator returned an empty wire value")
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("allocator reused %q", value)
		}
		seen[value] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("allocated %d values, want %d", len(seen), count)
	}

	first := NewDefault().Allocate()
	second := NewDefault().Allocate()
	if first == second {
		t.Fatalf("independent services allocated the same value %q", first)
	}
}

func TestServiceSubstitutesOnlyTheFirstExactTextDelta(t *testing.T) {
	const sentinel = "\x00textseed:test:1"
	const seedValue = ""
	inner := newFakeSession()
	service := newTestService(t)
	wrapper := service.WrapSession(context.Background(), inner, sentinel, textseed.Seed{Value: seedValue, Present: true})
	t.Cleanup(func() {
		if err := wrapper.Close(); err != nil {
			t.Errorf("cleanup close: %v", err)
		}
	})

	original := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(sentinel)}
	if !wrapper.Send(context.Background(), original) {
		t.Fatal("first sentinel send was rejected")
	}
	originalValue, ok := original.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("caller message value type = %T", original.Value)
	}
	if originalValue.Content != sentinel {
		t.Fatalf("caller message was mutated to %q", originalValue.Content)
	}
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(sentinel)},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("different")},
		{Type: messages.StreamTypeReasoningDelta, Value: messages.NewReasoningDeltaValue(sentinel)},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewReasoningDeltaValue(sentinel)},
	} {
		if !wrapper.Send(context.Background(), msg) {
			t.Fatal("pass-through send was rejected")
		}
	}

	inner.mu.Lock()
	sent := append([]messages.StreamMessage(nil), inner.sent...)
	inner.mu.Unlock()
	if len(sent) != 5 {
		t.Fatalf("sent message count = %d, want 5", len(sent))
	}
	first, ok := sent[0].Value.(*messages.TextDeltaValue)
	if !ok || first.Content != seedValue {
		t.Fatalf("first content = %#v, want empty seed", sent[0].Value)
	}
	second, ok := sent[1].Value.(*messages.TextDeltaValue)
	if !ok || second.Content != sentinel {
		t.Fatalf("duplicate sentinel content = %#v, want sentinel", sent[1].Value)
	}
	if got, ok := sent[2].Value.(*messages.TextDeltaValue); !ok || got.Content != "different" {
		t.Fatalf("wrong-value content = %#v", sent[2].Value)
	}
	if got, ok := sent[3].Value.(*messages.ReasoningDeltaValue); !ok || got.Content != sentinel {
		t.Fatalf("wrong-type content = %#v", sent[3].Value)
	}
	if got, ok := sent[4].Value.(*messages.ReasoningDeltaValue); !ok || got.Content != sentinel {
		t.Fatalf("wrong-payload content = %#v", sent[4].Value)
	}
}

func TestServiceForwardsLifecycleAndOptionalCapabilities(t *testing.T) {
	inner := newFakeSession()
	inner.response = true
	terminal := errors.New("provider terminal")
	inner.terminal = terminal
	wrapper := newTestService(t).WrapSession(context.Background(), inner, "sentinel", textseed.Seed{})
	if wrapper.Receive().Cap() != textseed.ReceiveCapacity {
		t.Fatalf("receive capacity = %d, want %d", wrapper.Receive().Cap(), textseed.ReceiveCapacity)
	}

	message := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("inbound")}
	if !inner.incoming.Write(context.Background(), message) {
		t.Fatal("fake inbound write failed")
	}
	got, ok := wrapper.Receive().ReadBlocking(wrapper.Done())
	gotValue, gotValueOK := got.Value.(*messages.TextDeltaValue)
	if !gotValueOK || gotValue.Content != "inbound" {
		t.Fatalf("forwarded inbound message = %#v, ok=%v", got, ok)
	}
	if !wrapper.SupportsResponseRequests() {
		t.Fatal("response capability was not forwarded")
	}
	if outcome := wrapper.RequestResponse(context.Background()); !outcome.OK() {
		t.Fatalf("response request outcome = %#v", outcome)
	}
	if !wrapper.SupportsCompleteMessages() || !wrapper.SendMessage(context.Background(), messages.Message{}) {
		t.Fatal("complete-message capability was not forwarded")
	}
	if !wrapper.SupportsCompleteMessagesWithoutResponse() || !wrapper.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("without-response capability was not forwarded")
	}
	if got := wrapper.TerminalError(); !errors.Is(got, terminal) {
		t.Fatalf("terminal error identity changed: got %v, want %v", got, terminal)
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-wrapper.Done():
	case <-time.After(time.Second):
		t.Fatal("wrapped session did not finish after close")
	}
}

func TestServiceLeavesOmittedSeedUntouched(t *testing.T) {
	inner := newFakeSession()
	service := newTestService(t)
	wrapper := service.WrapSession(context.Background(), inner, "sentinel", textseed.Seed{})
	t.Cleanup(func() {
		if err := wrapper.Close(); err != nil {
			t.Errorf("cleanup close: %v", err)
		}
	})
	if !wrapper.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("sentinel"),
	}) {
		t.Fatal("omitted-seed send rejected")
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	got, ok := inner.sent[0].Value.(*messages.TextDeltaValue)
	if !ok || got.Content != "sentinel" {
		t.Fatalf("omitted-seed content = %#v, want sentinel", inner.sent[0].Value)
	}
}

func TestServiceCancellationClosesTheInnerSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := newFakeSession()
	wrapper := newTestService(t).WrapSession(ctx, inner, "sentinel", textseed.Seed{})
	cancel()
	select {
	case <-inner.Done():
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close the inner session")
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestServicePreservesSessionSendOutcome(t *testing.T) {
	inner := newFakeSession()
	writeErr := errors.New("send failed")
	inner.outcome = messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: writeErr}
	wrapper := newTestService(t).WrapSession(context.Background(), inner, "sentinel", textseed.Seed{})
	t.Cleanup(func() {
		if err := wrapper.Close(); err != nil {
			t.Errorf("cleanup close: %v", err)
		}
	})
	outcome := wrapper.SendWithOutcome(context.Background(), messages.StreamMessage{})
	if !errors.Is(outcome.Err, writeErr) || outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("outcome = %#v, want retained failure identity", outcome)
	}
}

func TestServiceWrapsInferencer(t *testing.T) {
	inner := newFakeSession()
	service := newTestService(t)
	wrapper, err := service.WrapInferencer(fakeInferencer{session: inner}, "unused", textseed.Seed{}).ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if !wrapper.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("not-a-sentinel")}) {
		t.Fatal("wrapped inferencer session rejected send")
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 1, w.err }

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestServiceOutputRetainsFirstWriteErrorAndUnderlyingCount(t *testing.T) {
	firstErr := errors.New("first write")
	output := newTestService(t).NewOutput(failWriter{err: firstErr})
	if n, err := output.Write([]byte("abcd")); n != 1 || !errors.Is(err, firstErr) {
		t.Fatalf("first write = (%d, %v), want (1, first error)", n, err)
	}
	if n, err := output.Write([]byte("abcd")); n != 0 || !errors.Is(err, firstErr) {
		t.Fatalf("later write = (%d, %v), want retained first error", n, err)
	}
	if err := output.Err(); !errors.Is(err, firstErr) {
		t.Fatalf("retained error = %v, want first error", err)
	}

	short := newTestService(t).NewOutput(shortWriter{})
	if n, err := short.Write([]byte("abcd")); n != 3 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write = (%d, %v), want (3, io.ErrShortWrite)", n, err)
	}
	if err := short.Err(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short-write error = %v, want io.ErrShortWrite", err)
	}
}

func TestServiceAllocatorCanBeExplicitlyInjected(t *testing.T) {
	var next atomic.Uint64
	service := New(textseed.AllocatorFunc(func() string {
		return "injected:" + string(rune('a'+next.Add(1)))
	}))
	if got := service.Allocate(); got != "injected:b" {
		t.Fatalf("first injected allocation = %q", got)
	}
}
