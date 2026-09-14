package agentruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type stubPlanSessionInferencer struct{}

func (stubPlanSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("stubPlanSessionInferencer never connects")
}

type roundTripSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once

	mu         sync.Mutex
	sent       []messages.StreamMessage
	sentEvents chan messages.StreamMessage
}

var _ messages.Session = (*roundTripSession)(nil)

func newRoundTripSession() *roundTripSession {
	return &roundTripSession{
		recv:       messages.NewTypedBuffer[messages.StreamMessage](64),
		done:       make(chan struct{}),
		sentEvents: make(chan messages.StreamMessage, 64),
	}
}

func (s *roundTripSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	select {
	case s.sentEvents <- msg:
	default:
	}
	return true
}

func (s *roundTripSession) waitForSent(ctx context.Context, want messages.StreamMessageType) bool {
	for {
		select {
		case msg := <-s.sentEvents:
			if msg.Type == want {
				return true
			}
		case <-ctx.Done():
			return false
		}
	}
}

func (s *roundTripSession) SentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *roundTripSession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *roundTripSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }
func (s *roundTripSession) Done() <-chan struct{}                                  { return s.done }
func (s *roundTripSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type signalingBuffer struct {
	lockedBuffer
	observed chan string
}

func newSignalingBuffer() *signalingBuffer { return &signalingBuffer{observed: make(chan string, 512)} }

func (w *signalingBuffer) Write(p []byte) (int, error) {
	n, err := w.lockedBuffer.Write(p)
	select {
	case w.observed <- string(p):
	default:
	}
	return n, err
}

func (w *signalingBuffer) waitForOutput(want string, bound time.Duration) bool {
	deadline := time.After(bound)
	for {
		select {
		case chunk := <-w.observed:
			if strings.Contains(chunk, want) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

type scriptedTurn struct {
	events []messages.StreamMessage
	after  string
}

type scriptedToolCallInferencer struct {
	turns          []scriptedTurn
	followUpText   string
	followUpGate   string
	followUpEvents []messages.StreamMessage
	out            *signalingBuffer
	runFinished    chan struct{}
	finishOnce     sync.Once
	sessionMu      sync.Mutex
	session        *roundTripSession
}

var _ messages.SessionInferencer = (*scriptedToolCallInferencer)(nil)

func newScriptedToolCallInferencer(out *signalingBuffer, followUp, followUpGate string, turns ...scriptedTurn) *scriptedToolCallInferencer {
	return &scriptedToolCallInferencer{turns: turns, followUpText: followUp, followUpGate: followUpGate, out: out, runFinished: make(chan struct{})}
}

func (i *scriptedToolCallInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newRoundTripSession()
	i.sessionMu.Lock()
	i.session = session
	i.sessionMu.Unlock()
	go i.driveScriptedSession(ctx, session)
	return session, nil
}

func (i *scriptedToolCallInferencer) driveScriptedSession(ctx context.Context, session *roundTripSession) {
	defer i.finishOnce.Do(func() { close(i.runFinished) })
	if !session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("roundtrip-session", "session")}) {
		return
	}
	if !i.driveScriptedTurns(ctx, session) || !i.waitForFollowUpGate() {
		return
	}
	i.sendFollowUp(ctx, session)
	_ = session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("roundtrip-session", "test complete")})
}

func (i *scriptedToolCallInferencer) driveScriptedTurns(ctx context.Context, session *roundTripSession) bool {
	for _, turn := range i.turns {
		if turn.after != "" && !i.out.waitForOutput(turn.after, 5*time.Second) {
			return false
		}
		for _, event := range turn.events {
			if !session.recv.Write(ctx, event) {
				return false
			}
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return false
		}
	}
	return true
}

func (i *scriptedToolCallInferencer) waitForFollowUpGate() bool {
	return i.followUpGate == "" || i.out.waitForOutput(i.followUpGate, 5*time.Second)
}

func (i *scriptedToolCallInferencer) sendFollowUp(ctx context.Context, session *roundTripSession) {
	if len(i.followUpEvents) > 0 {
		for _, event := range i.followUpEvents {
			_ = session.recv.Write(ctx, event)
		}
	} else {
		_ = session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
		_ = session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(i.followUpText)})
		_ = session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}
}

func (i *scriptedToolCallInferencer) sessionSnapshot() *roundTripSession {
	i.sessionMu.Lock()
	defer i.sessionMu.Unlock()
	return i.session
}

func toolCallEvents(callID, name, args string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: 0, Value: messages.NewToolCallStartValue(callID, name)},
		{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: 0, Value: messages.NewToolCallDeltaValue(args)},
		{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: 0, Value: messages.NewToolCallEndValue(callID, name, args)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
}

func parallelToolCallEvents(calls ...messages.ToolCall) []messages.StreamMessage {
	events := []messages.StreamMessage{{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}}
	for index, call := range calls {
		events = append(events,
			messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: index, Value: messages.NewToolCallStartValue(call.ID, call.Name)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: index, Value: messages.NewToolCallDeltaValue(call.Arguments)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: index, Value: messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments)},
		)
	}
	return append(events, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}
