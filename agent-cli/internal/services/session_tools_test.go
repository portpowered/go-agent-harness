package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type sessionToolExecutorFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f sessionToolExecutorFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

func TestSessionToolExecutor_PreservesCallAndCorrelatesSuccess(t *testing.T) {
	call := messages.ToolCall{
		ID:        "s14-call-42",
		Name:      "distinctive_session_tool",
		Arguments: `{"city":"São Paulo","units":"metric"}`,
	}
	var got messages.ToolCall
	var mu sync.Mutex
	inner := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		got = call
		mu.Unlock()
		return messages.ToolCallResponse{Content: "s14-result-payload"}, nil
	})

	response, err := newSessionToolExecutorWithTimeout(inner, time.Second).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got != call {
		t.Fatalf("executor call = %#v, want %#v", got, call)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name {
		t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, call.ID, call.Name)
	}
	if response.Content != "s14-result-payload" {
		t.Fatalf("response content = %q, want exact executor payload", response.Content)
	}
}

// TestSessionToolExecutor_ConvertsFailuresToCorrelatedResults pins the adapter
// contract each failure mode must satisfy before the stream-level table below
// drives the same modes through the in-memory session seam.
func TestSessionToolExecutor_ConvertsFailuresToCorrelatedResults(t *testing.T) {
	call := messages.ToolCall{ID: "s4-call-1", Name: "failure_case", Arguments: `{not-json}`}

	cases := []struct {
		name     string
		executor messages.ToolExecutor
		timeout  time.Duration
		contains string
	}{
		{
			name: "unknown tool",
			executor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				return messages.ToolCallResponse{}, errors.New(`tool "missing_tool" not found`)
			}),
			contains: "not found",
		},
		{
			name: "malformed arguments",
			executor: sessionToolExecutorFunc(func(_ context.Context, got messages.ToolCall) (messages.ToolCallResponse, error) {
				if got.Arguments != call.Arguments {
					return messages.ToolCallResponse{}, errors.New("arguments were changed")
				}
				return messages.ToolCallResponse{}, errors.New("failed to parse tool arguments")
			}),
			contains: "parse tool arguments",
		},
		{
			name: "executor panic",
			executor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				panic("panic detail must stay inside this invocation")
			}),
			contains: "panicked",
		},
		{
			name:    "executor timeout",
			timeout: 10 * time.Millisecond,
			executor: sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
				<-ctx.Done()
				return messages.ToolCallResponse{}, ctx.Err()
			}),
			contains: "timed out",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := newSessionToolExecutorWithTimeout(tc.executor, tc.timeout).Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			if response.ToolCallID != call.ID || response.Name != call.Name {
				t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, call.ID, call.Name)
			}
			if strings.TrimSpace(response.Content) == "" {
				t.Fatal("failure response content is empty")
			}
			if !strings.Contains(response.Content, tc.contains) {
				t.Fatalf("failure response = %q, want substring %q", response.Content, tc.contains)
			}
			if len(response.ContentParts) != 0 {
				t.Fatalf("failure response unexpectedly contained content parts: %#v", response.ContentParts)
			}
		})
	}
}

// TestSessionToolExecutor_CooperativeWorkerExitsAfterTimeout proves the
// context-cooperative execution contract: a worker that honors its context
// exits promptly once the adapter has returned on timeout, so no invocation
// goroutine outlives the call.
func TestSessionToolExecutor_CooperativeWorkerExitsAfterTimeout(t *testing.T) {
	workerExited := make(chan struct{})
	inner := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		defer close(workerExited)
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})

	response, err := newSessionToolExecutorWithTimeout(inner, 10*time.Millisecond).Execute(context.Background(), messages.ToolCall{ID: "t-call", Name: "t-tool"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if !strings.Contains(response.Content, "timed out") {
		t.Fatalf("response = %q, want timeout failure", response.Content)
	}

	select {
	case <-workerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout worker did not exit after Execute returned")
	}
}

func TestSessionToolExecutor_InteractivePolicyTimeoutClassifiesAndCancels(t *testing.T) {
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 15 * time.Millisecond
	policy, err := NewInteractiveToolPolicy(settings, []messages.ToolDefinition{{Name: "policy_slow_read"}})
	if err != nil {
		t.Fatalf("NewInteractiveToolPolicy: %v", err)
	}

	started := make(chan struct{})
	workerExited := make(chan struct{})
	inner := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		close(started)
		defer close(workerExited)
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})
	executor := newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntent(inner, &policy, 0, nil, nil)
	call := messages.ToolCall{ID: "policy-timeout-call", Name: "policy_slow_read", Arguments: `{}`}

	startedAt := time.Now()
	response, err := executor.Execute(context.Background(), call)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name {
		t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, call.ID, call.Name)
	}
	if !strings.Contains(response.Content, "classification="+SessionToolTimeoutClassification) {
		t.Fatalf("response = %q, want stable timeout classification %q", response.Content, SessionToolTimeoutClassification)
	}
	if !strings.Contains(response.Content, "tool execution timed out") {
		t.Fatalf("response = %q, want human-readable timeout explanation", response.Content)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("interactive timeout took %s, want configured deadline plus bounded scheduling tolerance", elapsed)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("policy-selected tool did not start")
	}
	select {
	case <-workerExited:
	case <-time.After(time.Second):
		t.Fatal("context-cooperative worker did not exit after policy timeout")
	}
}

func TestSessionToolExecutor_SIGINTCancellationDoesNotRecordFailedResult(t *testing.T) {
	intent := NewSessionCancellationIntent()
	intent.MarkSIGINT()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	observer := &sessionToolCancellationObserver{}
	inner := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})
	call := messages.ToolCall{ID: "sigint-call", Name: "sleep", Arguments: `{"duration":"30s"}`}

	_, err := newSessionToolExecutorWithTimeoutAndObserverAndCancellationIntent(inner, time.Second, observer, intent).Execute(ctx, call)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SIGINT tool cancellation error = %v, want context.Canceled", err)
	}
	if observer.calls != 1 {
		t.Fatalf("observed tool calls = %d, want one", observer.calls)
	}
	if observer.results != 0 {
		t.Fatalf("observed tool results = %d, want zero for a user-canceled invocation", observer.results)
	}
}

type sessionToolCancellationObserver struct {
	calls   int
	results int
}

func (o *sessionToolCancellationObserver) observeToolCall(messages.ToolCall) {
	o.calls++
}

func (o *sessionToolCancellationObserver) observeToolResult(messages.ToolCall, messages.ToolCallResponse, bool) {
	o.results++
}

func TestSessionToolExecutor_DefaultTimeoutExecutesSuccessfully(t *testing.T) {
	inner := sessionToolExecutorFunc(func(_ context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "ok"}, nil
	})

	response, err := newSessionToolExecutor(inner).Execute(context.Background(), messages.ToolCall{ID: "call", Name: "tool"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.Content != "ok" {
		t.Fatalf("response content = %q, want ok", response.Content)
	}
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
	mu           sync.Mutex
	session      *roundTripSession
	turns        []scriptedTurn
	followUpText string
	followUpGate string
	out          *signalingBuffer
	runFinished  chan struct{}
	finishOnce   sync.Once
	sessionMu    sync.Mutex
	session      *roundTripSession
}

var _ messages.SessionInferencer = (*scriptedToolCallInferencer)(nil)

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
	go func() {
		// The session deliberately stays open; the runner's MaxDuration ends
		// the run so all provider turns are drained deterministically.
		defer i.finishOnce.Do(func() { close(i.runFinished) })
		if !session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("roundtrip-session", "session"),
		}) {
			return
		}
		for _, turn := range i.turns {
			if turn.after != "" && !i.out.waitForOutput(turn.after, 5*time.Second) {
				return
			}
			for _, evt := range turn.events {
				if !session.recv.Write(ctx, evt) {
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
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeMessageStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageStartValue(),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue(i.followUpText),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("roundtrip-session", "test complete"),
		})
	}()
	return session, nil
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

func TestRunAgentLoopSession_InteractivePolicyTimeoutDeliversOneCorrelatedContinuation(t *testing.T) {
	const (
		callID   = "policy-timeout-call"
		toolName = "policy_slow_read"
	)
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 15 * time.Millisecond
	definitions := []messages.ToolDefinition{{Name: toolName}}
	policy, err := NewInteractiveToolPolicy(settings, definitions)
	if err != nil {
		t.Fatalf("NewInteractiveToolPolicy: %v", err)
	}

	workerExited := make(chan struct{})
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out, "post-policy-timeout continuation", SessionToolTimeoutClassification,
		scriptedTurn{events: toolCallEvents(callID, toolName, `{}`)},
	)
	executor := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		defer close(workerExited)
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})

	startedAt := time.Now()
	err = runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:           2 * time.Second,
		WaitForClose:          true,
		ToolExecutor:          executor,
		ToolDefinitions:       definitions,
		InteractiveToolPolicy: &policy,
	})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if elapsed > 750*time.Millisecond {
		t.Fatalf("policy timeout round trip took %s, want fast deadline plus deterministic tolerance", elapsed)
	}
	select {
	case <-workerExited:
	case <-time.After(time.Second):
		t.Fatal("context-cooperative timeout worker did not exit")
	}

	session := inferencer.sessionSnapshot()
	if session == nil {
		t.Fatal("scripted inferencer did not retain its session")
	}
	sent := session.sentSnapshot()
	var resultEnds []messages.StreamMessage
	var responseCreates []messages.StreamMessage
	for _, msg := range sent {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			resultEnds = append(resultEnds, msg)
		case messages.StreamTypeResponseCreate:
			responseCreates = append(responseCreates, msg)
		}
	}
	if len(resultEnds) != 1 {
		t.Fatalf("provider tool-result messages = %d, want exactly one; sent=%#v", len(resultEnds), sent)
	}
	if len(responseCreates) != 1 {
		t.Fatalf("provider continuation requests = %d, want exactly one; sent=%#v", len(responseCreates), sent)
	}
	result, ok := resultEnds[0].Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("provider tool-result value = %T, want *messages.ToolCallEndValue", resultEnds[0].Value)
	}
	if result.ToolCallID != callID || result.Name != toolName {
		t.Fatalf("provider tool-result correlation = (%q, %q), want (%q, %q)", result.ToolCallID, result.Name, callID, toolName)
	}
	if !strings.Contains(result.Arguments, "classification="+SessionToolTimeoutClassification) || !strings.Contains(result.Arguments, "tool execution timed out") {
		t.Fatalf("provider tool-result payload = %q, want stable classification and honest explanation", result.Arguments)
	}
	resultIndex := -1
	continuationIndex := -1
	for index, msg := range sent {
		if msg.Type == messages.StreamTypeToolCallEnd {
			resultIndex = index
		}
		if msg.Type == messages.StreamTypeResponseCreate {
			continuationIndex = index
		}
	}
	if resultIndex < 0 || continuationIndex <= resultIndex {
		t.Fatalf("provider boundary order = result %d, continuation %d; sent=%#v", resultIndex, continuationIndex, sent)
	}
	if !strings.Contains(out.String(), "post-policy-timeout continuation") {
		t.Fatalf("session did not produce a non-empty assistant continuation:\n%s", out.String())
	}
}

func TestRunAgentLoopSession_InteractiveTimeoutPreservesParallelSiblingResults(t *testing.T) {
	const (
		slowID   = "parallel-slow-call"
		fastID   = "parallel-fast-call"
		slowName = "policy_slow_read"
		fastName = "policy_fast_read"
	)
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 15 * time.Millisecond
	definitions := []messages.ToolDefinition{{Name: slowName}, {Name: fastName}}
	policy, err := NewInteractiveToolPolicy(settings, definitions)
	if err != nil {
		t.Fatalf("NewInteractiveToolPolicy: %v", err)
	}

	var callsMu sync.Mutex
	var calls []messages.ToolCall
	slowStarted := make(chan struct{})
	slowExited := make(chan struct{})
	out := newSignalingBuffer()
	slowCall := messages.ToolCall{ID: slowID, Name: slowName, Arguments: `{}`}
	fastCall := messages.ToolCall{ID: fastID, Name: fastName, Arguments: `{}`}
	inferencer := newScriptedToolCallInferencer(out, "parallel continuation", "parallel-fast-result",
		scriptedTurn{events: parallelToolCallEvents(slowCall, fastCall)},
	)
	executor := sessionToolExecutorFunc(func(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		callsMu.Lock()
		calls = append(calls, call)
		callsMu.Unlock()
		if call.ID == slowID {
			close(slowStarted)
			defer close(slowExited)
			<-ctx.Done()
			return messages.ToolCallResponse{}, ctx.Err()
		}
		return messages.ToolCallResponse{Content: "parallel-fast-result"}, nil
	})

	err = runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:           2 * time.Second,
		WaitForClose:          true,
		ToolExecutor:          executor,
		ToolDefinitions:       definitions,
		InteractiveToolPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow sibling did not start")
	}
	select {
	case <-slowExited:
	case <-time.After(time.Second):
		t.Fatal("slow sibling did not observe its independent timeout")
	}
	callsMu.Lock()
	gotCalls := append([]messages.ToolCall(nil), calls...)
	callsMu.Unlock()
	if len(gotCalls) != 2 {
		t.Fatalf("parallel executor calls = %#v, want both independent calls", gotCalls)
	}
	seen := map[string]messages.ToolCall{}
	for _, call := range gotCalls {
		seen[call.ID] = call
	}
	if seen[slowID] != slowCall || seen[fastID] != fastCall {
		t.Fatalf("parallel executor calls = %#v, want exact slow/fast identities", gotCalls)
	}

	session := inferencer.sessionSnapshot()
	if session == nil {
		t.Fatal("scripted inferencer did not retain its session")
	}
	sent := session.sentSnapshot()
	var resultEnds []messages.StreamMessage
	var responseCreates []messages.StreamMessage
	for _, msg := range sent {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			resultEnds = append(resultEnds, msg)
		case messages.StreamTypeResponseCreate:
			responseCreates = append(responseCreates, msg)
		}
	}
	if len(resultEnds) != 2 {
		t.Fatalf("provider tool-result messages = %d, want one per call; sent=%#v", len(resultEnds), sent)
	}
	if len(responseCreates) != 1 {
		t.Fatalf("provider continuation requests = %d, want exactly one batch continuation; sent=%#v", len(responseCreates), sent)
	}
	for index, want := range []struct {
		callID  string
		name    string
		content string
	}{
		{callID: slowID, name: slowName, content: "classification=" + SessionToolTimeoutClassification},
		{callID: fastID, name: fastName, content: "parallel-fast-result"},
	} {
		result, ok := resultEnds[index].Value.(*messages.ToolCallEndValue)
		if !ok {
			t.Fatalf("provider result %d value = %T, want *messages.ToolCallEndValue", index, resultEnds[index].Value)
		}
		if result.ToolCallID != want.callID || result.Name != want.name {
			t.Fatalf("provider result %d correlation = (%q, %q), want (%q, %q)", index, result.ToolCallID, result.Name, want.callID, want.name)
		}
		if !strings.Contains(result.Arguments, want.content) {
			t.Fatalf("provider result %d payload = %q, want %q", index, result.Arguments, want.content)
		}
		if index == 0 && strings.Contains(result.Arguments, "parallel-fast-result") {
			t.Fatalf("slow timeout result was contaminated by fast sibling payload: %q", result.Arguments)
		}
	}
	if !strings.Contains(out.String(), "parallel-fast-result") || !strings.Contains(out.String(), "parallel continuation") {
		t.Fatalf("parallel session lost sibling success or continuation:\n%s", out.String())
	}
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

func TestRunAgentLoopSession_ExecutesScriptedCallsInOrderAndKeepsSessionUsable(t *testing.T) {
	const (
		firstID    = "s14-call-1"
		secondID   = "s14-call-2"
		toolName   = "distinctive_session_tool"
		firstArgs  = "{\"city\":\"S\\u00e3o Paulo\",\"units\":\"metric\"}"
		secondArgs = "{\"city\":\"Osaka\"}"
	)

	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out,
		"follow-up turn after both tool results",
		resultPayloadFor(1),
		scriptedTurn{events: toolCallEvents(firstID, toolName, firstArgs)},
		scriptedTurn{events: toolCallEvents(secondID, toolName, secondArgs), after: resultPayloadFor(0)},
	)
	executor := &recordingSessionExecutor{}

	err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:  2 * time.Second,
		WaitForClose: true,
		ToolExecutor: executor,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}

	calls := executor.recorded()
	if len(calls) != 2 {
		t.Fatalf("executor invocations = %d, want exactly 2 (calls=%#v)\noutput:\n%s", len(calls), calls, out.String())
	}
	want := []messages.ToolCall{
		{ID: firstID, Name: toolName, Arguments: firstArgs},
		{ID: secondID, Name: toolName, Arguments: secondArgs},
	}
	for i, w := range want {
		if calls[i] != w {
			t.Fatalf("invocation %d = %#v, want %#v (raw values must be preserved in order)", i, calls[i], w)
		}
	}

	output := out.String()
	if !strings.Contains(output, resultPayloadFor(0)) || !strings.Contains(output, resultPayloadFor(1)) {
		t.Fatalf("output missing correlated tool results:\n%s", output)
	}
	if !strings.Contains(output, "follow-up turn after both tool results") {
		t.Fatalf("session did not continue to a later model turn:\n%s", output)
	}
	if strings.Contains(output, "default tool executor") {
		t.Fatalf("loop reached the default executor instead of the composed one:\n%s", output)
	}
	// In duplex mode executed results feed the loop's conversation history
	// (ordering.UpdateWorldHistory) rather than being re-sent over session.Send;
	// the emitted RoleTool deltas plus the follow-up model turn prove the round
	// trip through the harness.
}

// TestRunAgentLoopSession_FailureTableKeepsSessionAlive is the S4-style table:
// every failure mode crosses the in-memory session seam exactly like a real
// provider call and must produce one non-empty correlated failure without
// terminating the session.
func TestRunAgentLoopSession_FailureTableKeepsSessionAlive(t *testing.T) {
	const (
		callID     = "s4-call-9"
		toolName   = "broken_session_tool"
		args       = `{not-json}`
		successTag = "SUCCESS-PAYLOAD-MUST-NOT-APPEAR"
	)

	cases := []struct {
		name     string
		timeout  time.Duration
		executor sessionToolExecutorFunc
		contains string
	}{
		{
			name: "unknown tool",
			executor: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				return messages.ToolCallResponse{}, errors.New(`tool "missing_tool" not found`)
			},
			contains: "not found",
		},
		{
			name: "malformed arguments",
			executor: func(_ context.Context, got messages.ToolCall) (messages.ToolCallResponse, error) {
				if got.Arguments != args {
					return messages.ToolCallResponse{}, errors.New("arguments were changed")
				}
				return messages.ToolCallResponse{}, errors.New("failed to parse tool arguments")
			},
			contains: "parse tool arguments",
		},
		{
			name: "executor panic",
			executor: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				panic("panic detail must stay inside this invocation")
			},
			contains: "panicked",
		},
		{
			name:    "executor timeout",
			timeout: 10 * time.Millisecond,
			executor: func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
				<-ctx.Done()
				return messages.ToolCallResponse{}, ctx.Err()
			},
			contains: "timed out",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			followUp := "recovered after " + tc.name
			out := newSignalingBuffer()
			inferencer := newScriptedToolCallInferencer(out, followUp, tc.contains,
				scriptedTurn{events: toolCallEvents(callID, toolName, args)},
			)

			attempts := 0
			var mu sync.Mutex
			executor := sessionToolExecutorFunc(func(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
				mu.Lock()
				attempts++
				current := attempts
				mu.Unlock()
				if current > 1 {
					return messages.ToolCallResponse{Content: successTag}, nil
				}
				return tc.executor(ctx, call)
			})

			err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
				MaxDuration:          2 * time.Second,
				WaitForClose:         true,
				ToolExecutor:         executor,
				ToolExecutionTimeout: tc.timeout,
			})
			if err != nil {
				t.Fatalf("%s: failing tool must not terminate the session, got: %v\noutput:\n%s", tc.name, err, out.String())
			}

			mu.Lock()
			defer mu.Unlock()
			if attempts != 1 {
				t.Fatalf("%s: executor attempts = %d, want exactly 1\noutput:\n%s", tc.name, attempts, out.String())
			}
			output := out.String()
			if !strings.Contains(output, tc.contains) {
				t.Fatalf("%s: provider-visible failure missing for call %q:\n%s", tc.name, callID, output)
			}
			if !strings.Contains(output, `"`+toolName+`" failed`) {
				t.Fatalf("%s: failure text lost the original tool name correlation:\n%s", tc.name, output)
			}
			if strings.Contains(output, successTag) {
				t.Fatalf("%s: failed call unexpectedly produced a successful payload:\n%s", tc.name, output)
			}
			if !strings.Contains(output, followUp) {
				t.Fatalf("%s: session did not continue to a deterministic later event:\n%s", tc.name, output)
			}
		})
	}
}

// TestRunAgentLoopSession_TimeoutWorkerExitsBoundedly drives the cooperative
// timeout contract through the session seam: the adapter returns on its
// deadline, the honoring worker exits promptly afterwards, and the session
// keeps processing later events.
func TestRunAgentLoopSession_TimeoutWorkerExitsBoundedly(t *testing.T) {
	workerExited := make(chan struct{})
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out, "post-timeout continuation", "timed out",
		scriptedTurn{events: toolCallEvents("t-call-1", "slow_session_tool", "{}")},
	)

	executor := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		defer close(workerExited)
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})

	err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:          2 * time.Second,
		WaitForClose:         true,
		ToolExecutor:         executor,
		ToolExecutionTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "timed out") {
		t.Fatalf("output missing timeout failure:\n%s", out.String())
	}

	select {
	case <-workerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout worker did not exit after the adapter returned")
	}
	if !strings.Contains(out.String(), "post-timeout continuation") {
		t.Fatalf("session did not continue after the timed-out tool:\n%s", out.String())
	}
}

// TestSessionToolExecutor_DefaultWrapperAppliesProductionBound pins the
// legacy default wiring: newSessionToolExecutor — the compatibility constructor
// for non-policy callers — must impose the documented 60s
// defaultSessionToolExecutionTimeout bound. The proof reads the deadline handed
// to the inner executor, so it never waits out the bound.
func TestSessionToolExecutor_DefaultWrapperAppliesProductionBound(t *testing.T) {
	if defaultSessionToolExecutionTimeout != 60*time.Second {
		t.Fatalf("defaultSessionToolExecutionTimeout = %s, want the documented 60s production default", defaultSessionToolExecutionTimeout)
	}

	var mu sync.Mutex
	var deadlineOK bool
	var remaining time.Duration
	inner := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		deadline, ok := ctx.Deadline()
		deadlineOK = ok
		if ok {
			remaining = time.Until(deadline)
		}
		return messages.ToolCallResponse{Content: "ok"}, nil
	})

	response, err := newSessionToolExecutor(inner).Execute(context.Background(), messages.ToolCall{ID: "d-call", Name: "d-tool"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.Content != "ok" {
		t.Fatalf("response content = %q, want ok", response.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if !deadlineOK {
		t.Fatal("default wrapper executed the tool without any deadline")
	}
	// The synchronous Execute call bounds scheduling noise; the observed
	// window must be a fresh ~60s bound, not zero and not an override.
	if remaining <= 0 || remaining > defaultSessionToolExecutionTimeout {
		t.Fatalf("default wrapper inner deadline = %s from now, want within (0, %s]", remaining, defaultSessionToolExecutionTimeout)
	}
	if remaining < defaultSessionToolExecutionTimeout-time.Second {
		t.Fatalf("default wrapper inner deadline = %s from now, want within 1s of the full %s bound", remaining, defaultSessionToolExecutionTimeout)
	}
}

// stubPlanSessionInferencer satisfies messages.SessionInferencer so plan tests
// can take the injected-inferencer replay branch without provider config.
type stubPlanSessionInferencer struct{}

func (stubPlanSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("stubPlanSessionInferencer never connects")
}

var _ messages.SessionInferencer = stubPlanSessionInferencer{}

// TestPlanSessionRuntimeThreadsToolExecutorAndDeadlineOverride proves the
// exported SessionRunOptions seam crosses both the composed executor and the
// per-invocation deadline override into the duplex loop options, and that a
// zero override keeps the production default path.
func TestPlanSessionRuntimeThreadsToolExecutorAndDeadlineOverride(t *testing.T) {
	executor := sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, nil
	})

	plan, err := planSessionRuntime(SessionRunOptions{
		ReplayPath:           "unused.json",
		SessionInferencer:    stubPlanSessionInferencer{},
		ToolExecutor:         executor,
		ToolExecutionTimeout: 7 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("planSessionRuntime: %v", err)
	}
	if plan.loop.ToolExecutor == nil {
		t.Fatal("plan dropped the composed tool executor")
	}
	if plan.loop.ToolExecutionTimeout != 7*time.Millisecond {
		t.Fatalf("plan.loop.ToolExecutionTimeout = %s, want 7ms", plan.loop.ToolExecutionTimeout)
	}

	defaultPlan, err := planSessionRuntime(SessionRunOptions{
		ReplayPath:        "unused.json",
		SessionInferencer: stubPlanSessionInferencer{},
		ToolExecutor:      executor,
	})
	if err != nil {
		t.Fatalf("planSessionRuntime without override: %v", err)
	}
	if defaultPlan.loop.ToolExecutor == nil {
		t.Fatal("plan dropped the composed tool executor without an override")
	}
	if defaultPlan.loop.ToolExecutionTimeout != 0 {
		t.Fatalf("plan.loop.ToolExecutionTimeout = %s without override, want 0 (production default path)", defaultPlan.loop.ToolExecutionTimeout)
	}
}
