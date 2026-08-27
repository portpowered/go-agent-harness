package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	parallelLifecycleAlphaID   = "call_lifecycle_alpha"
	parallelLifecycleAlphaName = "lookup_weather"
	parallelLifecycleAlphaArgs = `{"city":"Lisbon"}`

	parallelLifecycleBravoID   = "call_lifecycle_bravo"
	parallelLifecycleBravoName = "lookup_time"
	parallelLifecycleBravoArgs = `{"zone":"UTC"}`
)

var parallelLifecycleResultContent = map[string]string{
	parallelLifecycleAlphaID: `{"temperature_c":24,"origin":"alpha"}`,
	parallelLifecycleBravoID: `{"utc":"12:34","origin":"bravo"}`,
}

var parallelLifecycleRequestOrder = []string{
	parallelLifecycleAlphaID,
	parallelLifecycleBravoID,
}

// parallelLifecycleSession is a provider-shaped session double used through
// the ordinary session command composition boundary. One provider response
// carries two distinct calls. The second result can be held after the
// provider-facing send begins but before that send is reported as accepted,
// which creates a deterministic per-ID lifecycle checkpoint.
type parallelLifecycleSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	blockedResultID   string
	rejectedResultID  string
	rejectedResultErr error

	closeOnce sync.Once

	mu                 sync.Mutex
	responseCount      int
	sent               []messages.StreamMessage
	lifecycle          []string
	accepted           map[string]chan struct{}
	acceptedOnce       map[string]*sync.Once
	blockedResultStart map[string]chan struct{}
	blockedResultOnce  map[string]*sync.Once
	resultRelease      map[string]chan struct{}
}

func newParallelLifecycleSession(blockedResultID, rejectedResultID string) *parallelLifecycleSession {
	accepted := make(map[string]chan struct{}, len(parallelLifecycleRequestOrder))
	acceptedOnce := make(map[string]*sync.Once, len(parallelLifecycleRequestOrder))
	blockedResultStart := make(map[string]chan struct{}, len(parallelLifecycleRequestOrder))
	blockedResultOnce := make(map[string]*sync.Once, len(parallelLifecycleRequestOrder))
	resultRelease := make(map[string]chan struct{}, len(parallelLifecycleRequestOrder))
	for _, id := range parallelLifecycleRequestOrder {
		accepted[id] = make(chan struct{})
		acceptedOnce[id] = &sync.Once{}
		blockedResultStart[id] = make(chan struct{})
		blockedResultOnce[id] = &sync.Once{}
		resultRelease[id] = make(chan struct{})
	}
	return &parallelLifecycleSession{
		recv:               messages.NewTypedBuffer[messages.StreamMessage](64),
		done:               make(chan struct{}),
		blockedResultID:    blockedResultID,
		rejectedResultID:   rejectedResultID,
		accepted:           accepted,
		acceptedOnce:       acceptedOnce,
		blockedResultStart: blockedResultStart,
		blockedResultOnce:  blockedResultOnce,
		resultRelease:      resultRelease,
	}
}

func (s *parallelLifecycleSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *parallelLifecycleSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return parallelLifecycleContextOutcome(err)
	}

	s.mu.Lock()
	s.sent = append(s.sent, msg)
	responseNumber := 0
	if msg.Type == messages.StreamTypeMessageEnd {
		s.responseCount++
		responseNumber = s.responseCount
	}
	s.mu.Unlock()

	if responseNumber == 1 {
		s.emitTwoCallResponse()
	}

	if msg.Type != messages.StreamTypeToolCallEnd {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	value, ok := msg.Value.(*messages.ToolCallEndValue)
	if !ok || value == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: errors.New("parallel lifecycle result has no typed value")}
	}

	if value.ToolCallID == s.blockedResultID {
		if once := s.blockedResultOnce[value.ToolCallID]; once != nil {
			once.Do(func() { close(s.blockedResultStart[value.ToolCallID]) })
		}
		select {
		case <-s.resultRelease[value.ToolCallID]:
		case <-ctx.Done():
			return parallelLifecycleContextOutcome(ctx.Err())
		}
	}
	if value.ToolCallID == s.rejectedResultID {
		return messages.SessionSendOutcome{
			Status: messages.SessionSendBufferFull,
			Err:    s.rejectedResultErr,
		}
	}

	if once := s.acceptedOnce[value.ToolCallID]; once != nil {
		once.Do(func() {
			s.recordLifecycle("result_accepted:" + value.ToolCallID)
			close(s.accepted[value.ToolCallID])
		})
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func parallelLifecycleContextOutcome(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *parallelLifecycleSession) emitTwoCallResponse() {
	for _, call := range []struct {
		id, name, args string
	}{
		{parallelLifecycleAlphaID, parallelLifecycleAlphaName, parallelLifecycleAlphaArgs},
		{parallelLifecycleBravoID, parallelLifecycleBravoName, parallelLifecycleBravoArgs},
	} {
		for _, msg := range []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ToolCallId: call.id, Value: messages.NewToolCallStartValue(call.id, call.name)},
			{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: call.id, Value: messages.NewToolCallEndValue(call.id, call.name, call.args)},
		} {
			s.recv.Write(context.Background(), msg)
		}
	}
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
	} {
		s.recv.Write(context.Background(), msg)
	}
	s.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

func (s *parallelLifecycleSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *parallelLifecycleSession) Done() <-chan struct{} { return s.done }

func (s *parallelLifecycleSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *parallelLifecycleSession) acceptedSignal(callID string) <-chan struct{} {
	return s.accepted[callID]
}

func (s *parallelLifecycleSession) blockedResultStartedSignal(callID string) <-chan struct{} {
	return s.blockedResultStart[callID]
}

func (s *parallelLifecycleSession) releaseResult(callID string) {
	select {
	case <-s.resultRelease[callID]:
	default:
		close(s.resultRelease[callID])
	}
}

func (s *parallelLifecycleSession) recordLifecycle(event string) {
	s.mu.Lock()
	s.lifecycle = append(s.lifecycle, event)
	s.mu.Unlock()
}

func (s *parallelLifecycleSession) lifecycleSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lifecycle...)
}

func (s *parallelLifecycleSession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

type parallelLifecycleInferencer struct {
	ready   chan struct{}
	mu      sync.Mutex
	session *parallelLifecycleSession
}

func newParallelLifecycleInferencer(session *parallelLifecycleSession) *parallelLifecycleInferencer {
	return &parallelLifecycleInferencer{ready: make(chan struct{}), session: session}
}

func (i *parallelLifecycleInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("parallel-tool-lifecycle", "test"),
	})
	i.session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("parallel-tool-lifecycle"),
	})
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	close(i.ready)
	return session, nil
}

func (i *parallelLifecycleInferencer) connectedSession() *parallelLifecycleSession {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.session
}

type parallelLifecycleExecutor struct {
	mu             sync.Mutex
	calls          []messages.ToolCall
	completions    []string
	started        map[string]struct{}
	allStarted     chan struct{}
	allStartedOnce sync.Once
	release        map[string]chan struct{}
	bravoCompleted chan struct{}
	bravoOnce      sync.Once
}

func newParallelLifecycleExecutor() *parallelLifecycleExecutor {
	release := make(map[string]chan struct{}, len(parallelLifecycleRequestOrder))
	for _, id := range parallelLifecycleRequestOrder {
		release[id] = make(chan struct{})
	}
	return &parallelLifecycleExecutor{
		started:        make(map[string]struct{}, len(parallelLifecycleRequestOrder)),
		allStarted:     make(chan struct{}),
		release:        release,
		bravoCompleted: make(chan struct{}),
	}
}

func (e *parallelLifecycleExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.started[call.ID] = struct{}{}
	allStarted := len(e.started) == len(parallelLifecycleRequestOrder)
	release, known := e.release[call.ID]
	e.mu.Unlock()
	if allStarted {
		e.allStartedOnce.Do(func() { close(e.allStarted) })
	}
	if !known {
		return messages.ToolCallResponse{}, fmt.Errorf("unexpected parallel lifecycle call ID %q", call.ID)
	}

	select {
	case <-e.allStarted:
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
	select {
	case <-release:
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}

	if call.ID == parallelLifecycleBravoID {
		e.mu.Lock()
		e.completions = append(e.completions, call.ID)
		e.mu.Unlock()
		e.bravoOnce.Do(func() { close(e.bravoCompleted) })
	} else {
		select {
		case <-e.bravoCompleted:
		case <-ctx.Done():
			return messages.ToolCallResponse{}, ctx.Err()
		}
		e.mu.Lock()
		e.completions = append(e.completions, call.ID)
		e.mu.Unlock()
	}
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    parallelLifecycleResultContent[call.ID],
	}, nil
}

func (e *parallelLifecycleExecutor) releaseCall(callID string) {
	select {
	case <-e.release[callID]:
	default:
		close(e.release[callID])
	}
}

func (e *parallelLifecycleExecutor) releaseAll() {
	for _, id := range parallelLifecycleRequestOrder {
		e.releaseCall(id)
	}
}

func (e *parallelLifecycleExecutor) callsSnapshot() (calls []messages.ToolCall, completions []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...), append([]string(nil), e.completions...)
}

type parallelLifecycleObservation struct {
	inferencer *parallelLifecycleInferencer

	localCloseOnce  sync.Once
	localClose      chan struct{}
	mu              sync.Mutex
	localCloseCount int
}

func newParallelLifecycleObservation(inferencer *parallelLifecycleInferencer) *parallelLifecycleObservation {
	return &parallelLifecycleObservation{
		inferencer: inferencer,
		localClose: make(chan struct{}),
	}
}

func (o *parallelLifecycleObservation) observe(msg messages.StreamMessage) {
	if msg.Type != messages.StreamTypeSessionClose {
		return
	}
	if session := o.inferencer.connectedSession(); session != nil {
		session.recordLifecycle("client_close")
	}
	o.mu.Lock()
	o.localCloseCount++
	o.mu.Unlock()
	o.localCloseOnce.Do(func() { close(o.localClose) })
}

func (o *parallelLifecycleObservation) closeCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.localCloseCount
}

func waitParallelLifecycleSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", name)
	}
}

func assertParallelLifecycleCalls(t *testing.T, calls []messages.ToolCall) {
	t.Helper()
	if len(calls) != len(parallelLifecycleRequestOrder) {
		t.Fatalf("executor observed %d calls, want exactly %d: %#v", len(calls), len(parallelLifecycleRequestOrder), calls)
	}
	seen := map[string]int{}
	for _, call := range calls {
		seen[call.ID]++
		wantName, wantArgs := parallelLifecycleIdentity(call.ID)
		if call.Name != wantName || call.Arguments != wantArgs {
			t.Fatalf("executor call %#v has wrong identity, want ID %q name %q args %q", call, call.ID, wantName, wantArgs)
		}
	}
	for _, id := range parallelLifecycleRequestOrder {
		if seen[id] != 1 {
			t.Fatalf("executor observed call %q %d times, want exactly once; calls=%#v", id, seen[id], calls)
		}
	}
}

func parallelLifecycleIdentity(callID string) (name, args string) {
	if callID == parallelLifecycleAlphaID {
		return parallelLifecycleAlphaName, parallelLifecycleAlphaArgs
	}
	return parallelLifecycleBravoName, parallelLifecycleBravoArgs
}

func assertParallelLifecycleResults(t *testing.T, sent []messages.StreamMessage, wantIDs ...string) {
	t.Helper()
	want := make(map[string]struct{}, len(wantIDs))
	for _, id := range wantIDs {
		want[id] = struct{}{}
	}
	counts := make(map[string]int, len(want))
	for _, msg := range sent {
		if msg.Type != messages.StreamTypeToolCallEnd {
			continue
		}
		value, ok := msg.Value.(*messages.ToolCallEndValue)
		if !ok || value == nil {
			t.Fatalf("provider result has value type %T, want *messages.ToolCallEndValue", msg.Value)
		}
		if _, expected := want[value.ToolCallID]; !expected {
			t.Fatalf("provider received result for unexpected call ID %q", value.ToolCallID)
		}
		counts[value.ToolCallID]++
		if value.Arguments != parallelLifecycleResultContent[value.ToolCallID] {
			t.Fatalf("provider result for %q carries %q, want %q", value.ToolCallID, value.Arguments, parallelLifecycleResultContent[value.ToolCallID])
		}
	}
	for _, id := range wantIDs {
		if counts[id] != 1 {
			t.Fatalf("provider received %d results for %q, want exactly one", counts[id], id)
		}
	}
}

func runParallelLifecycleCLI(t *testing.T, executor *parallelLifecycleExecutor, inferencer *parallelLifecycleInferencer, observation *parallelLifecycleObservation) <-chan error {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		executor,
		&mockInferencer{response: "stateless inferencer should not be called"},
		inferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	agentCLI.SetSessionStreamObserver(observation.observe)
	rootCmd := agentCLI.Generate()
	writer := NewTestWriter()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", t.TempDir(),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	runErr := make(chan error, 1)
	go func() {
		defer cancel()
		runErr <- rootCmd.ExecuteContext(ctx)
	}()
	return runErr
}

// TestSessionCommand_OverlappingToolResultsWaitIndependently proves the
// scheduled session lifecycle for two real provider calls through the shipped
// CLI command. Both executions are in flight before either can finish. They
// are released in reverse order; the first accepted result is followed by a
// blocked second provider send, and exactly one client close follows only after
// that second result is accepted.
func TestSessionCommand_OverlappingToolResultsWaitIndependently(t *testing.T) {
	session := newParallelLifecycleSession(parallelLifecycleBravoID, "")
	inferencer := newParallelLifecycleInferencer(session)
	executor := newParallelLifecycleExecutor()
	observation := newParallelLifecycleObservation(inferencer)
	runErr := runParallelLifecycleCLI(t, executor, inferencer, observation)
	defer executor.releaseAll()

	waitParallelLifecycleSignal(t, inferencer.ready, "session connection")
	waitParallelLifecycleSignal(t, executor.allStarted, "both tool calls to be in flight")
	select {
	case <-observation.localClose:
		t.Fatal("client close was sent while both tool results were unresolved")
	default:
	}

	// Execution completion is deliberately reverse-request-order. ToolRunner
	// emits the completed batch in request order, so the provider accepts alpha
	// first while bravo's send is held at the provider boundary.
	executor.releaseCall(parallelLifecycleBravoID)
	executor.releaseCall(parallelLifecycleAlphaID)
	waitParallelLifecycleSignal(t, session.acceptedSignal(parallelLifecycleAlphaID), "first correlated result acceptance")
	waitParallelLifecycleSignal(t, session.blockedResultStartedSignal(parallelLifecycleBravoID), "second result send to reach its acceptance boundary")
	select {
	case <-observation.localClose:
		t.Fatal("client close was sent after one result while the other call remained unresolved")
	default:
	}

	session.releaseResult(parallelLifecycleBravoID)
	waitParallelLifecycleSignal(t, session.acceptedSignal(parallelLifecycleBravoID), "second correlated result acceptance")
	waitParallelLifecycleSignal(t, observation.localClose, "client close after both accepted results")

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("session command returned an error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session command did not finish after the final accepted result")
	}

	calls, completions := executor.callsSnapshot()
	assertParallelLifecycleCalls(t, calls)
	if want := []string{parallelLifecycleBravoID, parallelLifecycleAlphaID}; len(completions) != len(want) || completions[0] != want[0] || completions[1] != want[1] {
		t.Fatalf("execution completion order = %v, want reverse request order %v", completions, want)
	}
	assertParallelLifecycleResults(t, session.sentSnapshot(), parallelLifecycleAlphaID, parallelLifecycleBravoID)
	if got := observation.closeCount(); got != 1 {
		t.Fatalf("client close count = %d, want exactly one", got)
	}
	if got, want := session.lifecycleSnapshot(), []string{
		"result_accepted:" + parallelLifecycleAlphaID,
		"result_accepted:" + parallelLifecycleBravoID,
		"client_close",
	}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("lifecycle order = %v, want %v", got, want)
	}
}

// TestSessionParallelToolResultsTerminalFailureNamesOnlyRemainingCall covers
// the negative terminal path with the same two-call provider turn. Alpha is
// accepted, bravo's provider send is deliberately rejected, and the typed
// failure plus structured diagnostic must name bravo only.
func TestSessionParallelToolResultsTerminalFailureNamesOnlyRemainingCall(t *testing.T) {
	session := newParallelLifecycleSession("", parallelLifecycleBravoID)
	session.rejectedResultErr = errors.New("provider result buffer is full")
	inferencer := newParallelLifecycleInferencer(session)
	executor := newParallelLifecycleExecutor()
	observation := newParallelLifecycleObservation(inferencer)
	sink := &unresolvedToolDiagnosticSink{}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSession(ctx, io.Discard, services.SessionRunOptions{
			RecordPath:        "parallel-tool-result-terminal-failure.session.json",
			Provider:          "openai",
			Model:             "gpt-realtime",
			APIKey:            "test-key",
			SessionInferencer: inferencer,
			ToolExecutor:      executor,
			Diagnostics:       sink,
			StreamObserver:    observation.observe,
			AudioInputs: []services.ScheduledAudioInput{{
				AfterCompletedTurns: 0,
				PCM:                 []byte{1, 2, 3, 4},
				EndOfTurn:           true,
			}},
		})
	}()
	defer executor.releaseAll()

	waitParallelLifecycleSignal(t, inferencer.ready, "terminal-path session connection")
	waitParallelLifecycleSignal(t, executor.allStarted, "terminal-path tool calls to be in flight")
	executor.releaseCall(parallelLifecycleBravoID)
	executor.releaseCall(parallelLifecycleAlphaID)
	waitParallelLifecycleSignal(t, session.acceptedSignal(parallelLifecycleAlphaID), "accepted result before terminal failure")

	var err error
	select {
	case err = <-runErr:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal-path session did not return after the rejected result send")
	}
	if err == nil {
		t.Fatal("terminal-path session returned nil with one unresolved tool result")
	}
	var unresolved *services.SessionUnresolvedToolResultsError
	if !errors.As(err, &unresolved) {
		t.Fatalf("terminal-path error = %v, want SessionUnresolvedToolResultsError", err)
	}
	if !errors.Is(err, services.ErrSessionUnresolvedToolResults) {
		t.Fatalf("terminal-path error = %v, want stable unresolved-result sentinel", err)
	}
	if got := unresolved.UnresolvedCallIDs(); len(got) != 1 || got[0] != parallelLifecycleBravoID {
		t.Fatalf("terminal-path unresolved IDs = %v, want only [%s]", got, parallelLifecycleBravoID)
	}
	if got := unresolved.SendStatuses[parallelLifecycleBravoID]; got != messages.SessionSendBufferFull {
		t.Fatalf("terminal-path send status = %q, want %q", got, messages.SessionSendBufferFull)
	}
	if !strings.Contains(err.Error(), parallelLifecycleBravoID) || strings.Contains(err.Error(), parallelLifecycleAlphaID) {
		t.Fatalf("terminal-path human error = %q, want only remaining call ID %q", err, parallelLifecycleBravoID)
	}

	failures := sink.failureRecords()
	if len(failures) != 1 {
		t.Fatalf("terminal-path failure diagnostic count = %d, want exactly one", len(failures))
	}
	fields := failures[0].Fields
	if fields[services.SessionDiagnosticFieldUnresolvedToolResultCount] != "1" {
		t.Fatalf("terminal-path unresolved count = %q, want 1", fields[services.SessionDiagnosticFieldUnresolvedToolResultCount])
	}
	if fields[services.SessionDiagnosticFieldUnresolvedToolCallIDs] != parallelLifecycleBravoID {
		t.Fatalf("terminal-path unresolved IDs diagnostic = %q, want only %s", fields[services.SessionDiagnosticFieldUnresolvedToolCallIDs], parallelLifecycleBravoID)
	}
	assertParallelLifecycleResults(t, session.sentSnapshot(), parallelLifecycleAlphaID, parallelLifecycleBravoID)
	if got := observation.closeCount(); got != 0 {
		t.Fatalf("terminal-path emitted %d clean client close events, want none", got)
	}
}

var _ messages.Session = (*parallelLifecycleSession)(nil)
var _ messages.SessionInferencer = (*parallelLifecycleInferencer)(nil)
var _ messages.ToolExecutor = (*parallelLifecycleExecutor)(nil)
