package agentruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// unwrappingInferencer exposes the provider inferencer behind a session-turn
// input decorator.
type unwrappingInferencer interface {
	Unwrap() messages.SessionInferencer
}

// providerToolBoundaries is the tool-result and continuation traffic a loop
// sent back to a scripted provider session.
type providerToolBoundaries struct {
	sent                         []messages.StreamMessage
	results                      []messages.StreamMessage
	continuations                []messages.StreamMessage
	lastResult, lastContinuation int
}

func sentToolBoundaries(t *testing.T, inferencer *scriptedToolCallInferencer) providerToolBoundaries {
	t.Helper()
	session := inferencer.sessionSnapshot()
	if session == nil {
		t.Fatal("scripted inferencer did not retain its session")
	}
	boundaries := providerToolBoundaries{sent: session.sentSnapshot(), lastResult: -1, lastContinuation: -1}
	for index, msg := range boundaries.sent {
		switch {
		case msg.Type == messages.StreamTypeToolCallEnd:
			boundaries.results = append(boundaries.results, msg)
			boundaries.lastResult = index
		case msg.Type == messages.StreamTypeResponseCreate:
			boundaries.continuations = append(boundaries.continuations, msg)
			boundaries.lastContinuation = index
		}
	}
	return boundaries
}

func (b providerToolBoundaries) requireCounts(t *testing.T, results, continuations int) {
	t.Helper()
	if len(b.results) != results {
		t.Fatalf("provider tool-result messages = %d, want %d; sent=%#v", len(b.results), results, b.sent)
	}
	if len(b.continuations) != continuations {
		t.Fatalf("provider continuation requests = %d, want %d; sent=%#v", len(b.continuations), continuations, b.sent)
	}
}

func toolResultValue(t *testing.T, msg messages.StreamMessage) *messages.ToolCallEndValue {
	t.Helper()
	result, ok := msg.Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("provider tool-result value = %T, want *messages.ToolCallEndValue", msg.Value)
	}
	return result
}

// requireExactToolCalls asserts the executor received exactly the wanted
// calls, in any order, with unchanged identities.
func requireExactToolCalls(t *testing.T, got []messages.ToolCall, want ...messages.ToolCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("executor calls = %#v, want %d independent calls", got, len(want))
	}
	seen := make(map[string]messages.ToolCall, len(got))
	for _, call := range got {
		seen[call.ID] = call
	}
	for _, call := range want {
		if seen[call.ID] != call {
			t.Fatalf("executor calls = %#v, want exact identity %#v", got, call)
		}
	}
}

// assertRecoveredToolFailure requires the provider-visible failure, its tool
// correlation, and a later continuation, and forbids a successful payload.
func assertRecoveredToolFailure(t *testing.T, output string, want []string, forbidden string) {
	t.Helper()
	for _, fragment := range want {
		if !strings.Contains(output, fragment) {
			t.Fatalf("session output missing %q:\n%s", fragment, output)
		}
	}
	if strings.Contains(output, forbidden) {
		t.Fatalf("failed call unexpectedly produced a successful payload:\n%s", output)
	}
}

// newTestSessionToolExecutor wraps inner through the production session-loop
// executor construction. A zero timeout selects the default interactive
// policy budget.
func newTestSessionToolExecutor(inner messages.ToolExecutor, timeout time.Duration) messages.ToolExecutor {
	return newSessionLoopToolExecutor(sessionLoopOptions{ToolExecutor: inner, ToolExecutionTimeout: timeout})
}

type sessionToolExecutorFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f sessionToolExecutorFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

type timeoutScreenPermissionExecutor struct {
	mu sync.Mutex

	permission  cliTools.DisplayPermission
	recheckErr  error
	recheckWait <-chan struct{}
	rechecks    int
	started     chan struct{}
	exited      chan struct{}
	startOnce   sync.Once
	exitOnce    sync.Once
}

func newTimeoutScreenPermissionExecutor(permission cliTools.DisplayPermission) *timeoutScreenPermissionExecutor {
	return &timeoutScreenPermissionExecutor{
		permission: permission,
		started:    make(chan struct{}),
		exited:     make(chan struct{}),
	}
}

func (e *timeoutScreenPermissionExecutor) Execute(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
	e.startOnce.Do(func() { close(e.started) })
	defer e.exitOnce.Do(func() { close(e.exited) })
	<-ctx.Done()
	return messages.ToolCallResponse{}, ctx.Err()
}

func (e *timeoutScreenPermissionExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	return true
}

func (e *timeoutScreenPermissionExecutor) RecheckScreenRecordingPermission(ctx context.Context) (cliTools.DisplayPermission, error) {
	e.mu.Lock()
	e.rechecks++
	permission := e.permission
	recheckErr := e.recheckErr
	wait := e.recheckWait
	e.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return cliTools.DisplayPermission{}, ctx.Err()
		}
	}
	return permission, recheckErr
}

func (e *timeoutScreenPermissionExecutor) setPermission(permission cliTools.DisplayPermission) {
	e.mu.Lock()
	e.permission = permission
	e.mu.Unlock()
}

func (e *timeoutScreenPermissionExecutor) recheckCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rechecks
}

// roundTripSession records everything the loop sends back to the provider.
type roundTripSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once

	mu         sync.Mutex
	sent       []messages.StreamMessage
	sentEvents chan messages.StreamMessage
}

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

func (s *roundTripSession) Done() <-chan struct{} { return s.done }

func (s *roundTripSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// signalingBuffer mirrors every write into a channel so the scripted
// inferencer can gate later provider turns on earlier results actually being
// observed in the drained loop output. Bounded channel synchronization only;
// no sleep polling anywhere.
type signalingBuffer struct {
	lockedBuffer
	observed chan string
}

func newSignalingBuffer() *signalingBuffer {
	return &signalingBuffer{observed: make(chan string, 512)}
}

func (w *signalingBuffer) Write(p []byte) (int, error) {
	n, err := w.lockedBuffer.Write(p)
	select {
	case w.observed <- string(p):
	default:
	}
	return n, err
}

// waitForOutput blocks until want appears in the drained output or the bound
// elapses.
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

// scriptedTurn is one scripted provider event batch plus the output substring
// that must be observed (already returned to the consumer) before the batch is
// emitted. An empty after means emit immediately.
type scriptedTurn struct {
	events []messages.StreamMessage
	after  string
}

// scriptedToolCallInferencer emits SESSION.OPEN, the scripted turns in causal
// order, and a final assistant turn that proves the session kept making
// progress after tool execution instead of terminating.
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

func newScriptedToolCallInferencer(out *signalingBuffer, followUp, followUpGate string, turns ...scriptedTurn) *scriptedToolCallInferencer {
	return &scriptedToolCallInferencer{
		turns:        turns,
		followUpText: followUp,
		followUpGate: followUpGate,
		out:          out,
		runFinished:  make(chan struct{}),
	}
}

func (i *scriptedToolCallInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newRoundTripSession()
	i.sessionMu.Lock()
	i.session = session
	i.sessionMu.Unlock()
	go i.stream(ctx, session)
	return session, nil
}

func (i *scriptedToolCallInferencer) stream(ctx context.Context, session *roundTripSession) {
	defer i.finishOnce.Do(func() { close(i.runFinished) })
	if !session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("roundtrip-session", "session")}) {
		return
	}
	for _, turn := range i.turns {
		if turn.after != "" && !i.out.waitForOutput(turn.after, 5*time.Second) {
			return
		}
		for _, event := range turn.events {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return
		}
	}
	if i.followUpGate != "" && !i.out.waitForOutput(i.followUpGate, 5*time.Second) {
		return
	}
	if !i.writeFollowUp(ctx, session) {
		return
	}
	session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("roundtrip-session", "test complete")})
}

func (i *scriptedToolCallInferencer) writeFollowUp(ctx context.Context, session *roundTripSession) bool {
	if len(i.followUpEvents) > 0 {
		for _, event := range i.followUpEvents {
			if !session.recv.Write(ctx, event) {
				return false
			}
		}
		return true
	}
	return session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}) &&
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(i.followUpText)}) &&
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
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
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
	}
	for index, call := range calls {
		events = append(events,
			messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: index, Value: messages.NewToolCallStartValue(call.ID, call.Name)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: index, Value: messages.NewToolCallDeltaValue(call.Arguments)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: index, Value: messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments)},
		)
	}
	return append(events, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

// recordingSessionExecutor records invocations in arrival order and answers
// each with a distinct correlated payload.
type recordingSessionExecutor struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func (e *recordingSessionExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	index := len(e.calls)
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return messages.ToolCallResponse{Content: resultPayloadFor(index)}, nil
}

func (e *recordingSessionExecutor) recorded() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}

func resultPayloadFor(index int) string {
	return "s14-result-payload-" + string(rune('1'+index))
}

// stubPlanSessionInferencer satisfies messages.SessionInferencer so plan tests
// can take the injected-inferencer replay branch without provider config.
type stubPlanSessionInferencer struct{}

func (stubPlanSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("stubPlanSessionInferencer never connects")
}
