package agentloop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type acknowledgementSession struct {
	mu                   sync.Mutex
	sent                 []messages.StreamMessage
	sentCh               chan messages.StreamMessage
	recv                 *messages.TypedBuffer[messages.StreamMessage]
	done                 chan struct{}
	doneOnce             sync.Once
	acknowledgementOpen  bool
	completeAck          bool
	acknowledgementStart chan struct{}
	acknowledgementEnd   chan struct{}
	ackStartOnce         sync.Once
	ackEndOnce           sync.Once
}

func newAcknowledgementSession(completeAck bool) *acknowledgementSession {
	return &acknowledgementSession{
		sentCh:               make(chan messages.StreamMessage, 32),
		recv:                 messages.NewTypedBuffer[messages.StreamMessage](128),
		done:                 make(chan struct{}),
		completeAck:          completeAck,
		acknowledgementStart: make(chan struct{}),
		acknowledgementEnd:   make(chan struct{}),
	}
}

func (s *acknowledgementSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	select {
	case s.sentCh <- msg:
	default:
	}

	switch msg.Type {
	case messages.StreamTypeResponseCreate:
		value, _ := msg.Value.(*messages.ResponseCreateValue)
		if value != nil && value.IsToolAcknowledgement() {
			s.mu.Lock()
			s.acknowledgementOpen = true
			s.mu.Unlock()
			s.emitAcknowledgement(s.completeAck)
		} else {
			s.mu.Lock()
			acknowledgementOpen := s.acknowledgementOpen
			s.mu.Unlock()
			if acknowledgementOpen {
				panic("normal continuation was sent while acknowledgement was open")
			}
			s.emitFinalResponse()
		}
	case messages.StreamTypeResponseCancel:
		s.mu.Lock()
		acknowledgementOpen := s.acknowledgementOpen
		s.acknowledgementOpen = false
		s.mu.Unlock()
		if acknowledgementOpen {
			s.emitAcknowledgementEnd()
		}
	}
	return true
}

func (s *acknowledgementSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *acknowledgementSession) Done() <-chan struct{} {
	return s.done
}

func (s *acknowledgementSession) Close() error {
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}

func (s *acknowledgementSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyOfSent := make([]messages.StreamMessage, len(s.sent))
	copy(copyOfSent, s.sent)
	return copyOfSent
}

func (s *acknowledgementSession) emitAcknowledgement(complete bool) {
	s.ackStartOnce.Do(func() { close(s.acknowledgementStart) })
	s.recv.Write(context.Background(), messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: "response-ack",
		Value:      messages.NewMessageStartValue(),
	})
	s.recv.Write(context.Background(), messages.StreamMessage{
		Type:       messages.StreamTypeAudioStart,
		Role:       messages.RoleAssistant,
		ResponseID: "response-ack",
		Value:      messages.NewAudioStartValue(),
	})
	s.recv.Write(context.Background(), messages.StreamMessage{
		Type:       messages.StreamTypeAudioDelta,
		Role:       messages.RoleAssistant,
		ResponseID: "response-ack",
		Value:      messages.NewAudioDeltaValue([]byte{1, 2, 3}),
	})
	if complete {
		s.emitAcknowledgementEnd()
	}
}

func (s *acknowledgementSession) emitAcknowledgementEnd() {
	s.ackEndOnce.Do(func() {
		s.mu.Lock()
		s.acknowledgementOpen = false
		s.mu.Unlock()
		s.recv.Write(context.Background(), messages.StreamMessage{
			Type:       messages.StreamTypeAudioEnd,
			Role:       messages.RoleAssistant,
			ResponseID: "response-ack",
			Value:      messages.NewAudioEndValue(),
		})
		s.recv.Write(context.Background(), messages.StreamMessage{
			Type:       messages.StreamTypeMessageEnd,
			Role:       messages.RoleAssistant,
			ResponseID: "response-ack",
			Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
		})
		close(s.acknowledgementEnd)
	})
}

func (s *acknowledgementSession) emitFinalResponse() {
	for _, msg := range []messages.StreamMessage{
		{
			Type:       messages.StreamTypeMessageStart,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewMessageStartValue(),
		},
		{
			Type:       messages.StreamTypeTextStart,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewTextStartValue(),
		},
		{
			Type:       messages.StreamTypeTextDelta,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewTextDeltaValue("done"),
		},
		{
			Type:       messages.StreamTypeTextEnd,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewTextEndValue(),
		},
		{
			Type:       messages.StreamTypeMessageEnd,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
		},
	} {
		s.recv.Write(context.Background(), msg)
	}
}

type acknowledgementSessionInferencer struct {
	session *acknowledgementSession
}

func (i acknowledgementSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type blockingToolExecutor struct {
	started     chan struct{}
	startedOnce sync.Once
	release     chan struct{}
}

func (e *blockingToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.startedOnce.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "tool result"}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func writeAcknowledgementToolCall(s *acknowledgementSession) {
	ctx := context.Background()
	s.recv.Write(ctx, messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: "response-tool",
		Value:      messages.NewMessageStartValue(),
	})
	s.recv.Write(ctx, messages.StreamMessage{
		Type:       messages.StreamTypeToolCallStart,
		Role:       messages.RoleAssistant,
		ToolCallId: "call-1",
		ResponseID: "response-tool",
		Value:      messages.NewToolCallStartValue("call-1", "slow"),
	})
	s.recv.Write(ctx, messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ToolCallId: "call-1",
		ResponseID: "response-tool",
		Value:      messages.NewToolCallEndValue("call-1", "slow", `{}`),
	})
	s.recv.Write(ctx, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-tool",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

func waitForAcknowledgementSent(t *testing.T, s *acknowledgementSession, predicate func(messages.StreamMessage) bool) messages.StreamMessage {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, msg := range s.sentMessages() {
			if predicate(msg) {
				return msg
			}
		}
		select {
		case <-s.sentCh:
		case <-deadline:
			t.Fatal("timed out waiting for session send")
		}
	}
}

func waitForAgentDelta(t *testing.T, ctx context.Context, al *AgentLoop, predicate func(messages.StreamMessage) bool) messages.StreamMessage {
	t.Helper()
	for {
		msg, err := al.Deltas().ReadContext(ctx)
		if err != nil {
			t.Fatalf("reading agent delta: %v", err)
		}
		if predicate(msg) {
			return msg
		}
	}
}

func newAcknowledgementTestLoop(t *testing.T, session *acknowledgementSession, executor messages.ToolExecutor) *AgentLoop {
	t.Helper()
	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(acknowledgementSessionInferencer{session: session}),
		WithToolExecutor(executor),
		WithTools([]messages.ToolDefinition{{Name: "slow", Description: "slow tool"}}),
		WithToolAcknowledgementPolicy(ToolAcknowledgementPolicy{
			Threshold: 20 * time.Millisecond,
			IsLongRunning: func(name string) bool {
				return name == "slow"
			},
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return al
}

func TestDuplexSession_LongRunningToolAcknowledgementPrecedesGroundedContinuation(t *testing.T) {
	session := newAcknowledgementSession(true)
	executor := &blockingToolExecutor{started: make(chan struct{}), release: make(chan struct{})}
	al := newAcknowledgementTestLoop(t, session, executor)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()
	writeAcknowledgementToolCall(session)

	select {
	case <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	ack := waitForAcknowledgementSent(t, session, func(msg messages.StreamMessage) bool {
		value, ok := msg.Value.(*messages.ResponseCreateValue)
		return msg.Type == messages.StreamTypeResponseCreate && ok && value.IsToolAcknowledgement()
	})
	ackValue := ack.Value.(*messages.ResponseCreateValue)
	if ackValue.Instructions == "" {
		t.Fatal("acknowledgement request omitted instructions")
	}
	ackAudio := waitForAgentDelta(t, contextWithTestTimeout(t), al, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeAudioDelta && msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement
	})
	if len(ackAudio.Value.(*messages.AudioDeltaValue).Content) == 0 {
		t.Fatal("acknowledgement audio was empty")
	}

	close(executor.release)
	waitForAcknowledgementSent(t, session, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeResponseCreate && msg.Value.(*messages.ResponseCreateValue).Purpose == ""
	})
	waitForAgentDelta(t, contextWithTestTimeout(t), al, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeMessageEnd && msg.ResponseID == "response-final"
	})

	sent := session.sentMessages()
	var responseCreates []messages.StreamMessage
	toolResultIndex := -1
	for i, msg := range sent {
		if msg.Type == messages.StreamTypeResponseCreate {
			responseCreates = append(responseCreates, msg)
		}
		if msg.Type == messages.StreamTypeToolCallEnd && toolResultIndex < 0 {
			toolResultIndex = i
		}
	}
	if len(responseCreates) != 2 {
		t.Fatalf("response.create sends = %d, want acknowledgement plus one continuation (%#v)", len(responseCreates), sent)
	}
	if responseCreates[0].Value.(*messages.ResponseCreateValue).Purpose != messages.ResponsePurposeToolAcknowledgement {
		t.Fatalf("first response.create = %#v, want acknowledgement", responseCreates[0].Value)
	}
	if responseCreates[1].Value.(*messages.ResponseCreateValue).Purpose != "" {
		t.Fatalf("second response.create = %#v, want ordinary continuation", responseCreates[1].Value)
	}
	if toolResultIndex < 0 || indexOfSentMessage(sent, responseCreates[1]) <= toolResultIndex {
		t.Fatalf("grounded continuation was not sent after its tool result: %#v", sent)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestDuplexSession_BargeInCancelsAcknowledgementAndPreservesToolResult(t *testing.T) {
	session := newAcknowledgementSession(false)
	executor := &blockingToolExecutor{started: make(chan struct{}), release: make(chan struct{})}
	al := newAcknowledgementTestLoop(t, session, executor)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()
	writeAcknowledgementToolCall(session)
	select {
	case <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	waitForAcknowledgementSent(t, session, func(msg messages.StreamMessage) bool {
		value, ok := msg.Value.(*messages.ResponseCreateValue)
		return msg.Type == messages.StreamTypeResponseCreate && ok && value.IsToolAcknowledgement()
	})
	select {
	case <-session.acknowledgementStart:
	case <-time.After(3 * time.Second):
		t.Fatal("acknowledgement response did not start")
	}
	waitForAgentDelta(t, contextWithTestTimeout(t), al, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeAudioDelta && msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement
	})

	if err := al.SendAudioInput(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatalf("SendAudioInput: %v", err)
	}
	waitForAcknowledgementSent(t, session, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeResponseCancel
	})
	waitForAcknowledgementSent(t, session, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeAudioDelta
	})

	close(executor.release)
	waitForAcknowledgementSent(t, session, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeResponseCreate && msg.Value.(*messages.ResponseCreateValue).Purpose == ""
	})
	waitForAgentDelta(t, contextWithTestTimeout(t), al, func(msg messages.StreamMessage) bool {
		return msg.Type == messages.StreamTypeMessageEnd && msg.ResponseID == "response-final"
	})

	sent := session.sentMessages()
	ackCount := 0
	cancelCount := 0
	toolResultCount := 0
	for _, msg := range sent {
		switch msg.Type {
		case messages.StreamTypeResponseCreate:
			if msg.Value.(*messages.ResponseCreateValue).IsToolAcknowledgement() {
				ackCount++
			}
		case messages.StreamTypeResponseCancel:
			cancelCount++
		case messages.StreamTypeToolCallEnd:
			toolResultCount++
		}
	}
	if ackCount != 1 || cancelCount != 1 || toolResultCount != 1 {
		t.Fatalf("post-barge sends = ack:%d cancel:%d tool_result:%d (%#v), want 1 each", ackCount, cancelCount, toolResultCount, sent)
	}
	if responseCancelIndex := indexOfSentType(sent, messages.StreamTypeResponseCancel); responseCancelIndex < 0 || indexOfSentType(sent, messages.StreamTypeAudioDelta) < responseCancelIndex {
		t.Fatalf("barge-in audio was not ordered after cancellation: %#v", sent)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func indexOfSentMessage(sent []messages.StreamMessage, target messages.StreamMessage) int {
	for i := range sent {
		if sent[i].Type == target.Type && sent[i].Value == target.Value {
			return i
		}
	}
	return -1
}

func indexOfSentType(sent []messages.StreamMessage, typ messages.StreamMessageType) int {
	for i, msg := range sent {
		if msg.Type == typ {
			return i
		}
	}
	return -1
}

func contextWithTestTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
