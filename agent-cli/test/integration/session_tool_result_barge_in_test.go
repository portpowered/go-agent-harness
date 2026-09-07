package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const sessionToolBargeInCallID = "call_barge_in_slow"
const sessionToolBargeInFinalResponseText = "second scheduled response"
const sessionToolBargeInFirstResponseID = "response_barge_in_tool"
const sessionToolBargeInContinuationResponseID = "response_barge_in_continuation"
const sessionToolBargeInFinalResponseID = "response_barge_in_final"
const sessionToolBargeInContinuationReadyID = "session-tool-barge-in-continuation-ready"

// sessionToolBargeInSession is a provider-shaped session double used through
// the shipped session command. The first completed response requests one
// tool, and the second completed response is emitted only after the next
// scheduled audio turn arrives. The executor barrier keeps the first result
// outstanding while that follow-on response completes.
type sessionToolBargeInSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	closeOnce             sync.Once
	firstResponseEndOnce  sync.Once
	resultAcceptedOnce    sync.Once
	continuationOnce      sync.Once
	continuationReadyOnce sync.Once
	secondAudioOnce       sync.Once
	finalResponseOnce     sync.Once

	mu                    sync.Mutex
	bargeIn               bool
	responseCount         int
	toolResultAccepted    bool
	secondResponsePending bool
	continuationRequested bool
	continuationEmitted   bool
	sent                  []messages.StreamMessage
	lifecycle             []string
	resultAccepted        chan struct{}
	secondAudioSent       chan struct{}
	finalResponseObserved chan struct{}
}

func newSessionToolBargeInSessionWithMode(bargeIn bool) *sessionToolBargeInSession {
	return &sessionToolBargeInSession{
		recv:                  messages.NewTypedBuffer[messages.StreamMessage](64),
		done:                  make(chan struct{}),
		bargeIn:               bargeIn,
		resultAccepted:        make(chan struct{}),
		secondAudioSent:       make(chan struct{}),
		finalResponseObserved: make(chan struct{}),
	}
}

func (s *sessionToolBargeInSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *sessionToolBargeInSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return sessionToolBargeInContextOutcome(err)
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()

	switch msg.Type {
	case messages.StreamTypeAudioDelta:
		s.mu.Lock()
		secondTurn := s.responseCount >= 1
		s.mu.Unlock()
		if secondTurn {
			s.secondAudioOnce.Do(func() {
				s.recordLifecycle("second_audio_sent")
				close(s.secondAudioSent)
			})
		}
	case messages.StreamTypeMessageEnd:
		s.mu.Lock()
		s.responseCount++
		response := s.responseCount
		bargeIn := s.bargeIn
		s.mu.Unlock()
		switch response {
		case 1:
			s.emitFirstResponse()
		case 2:
			if bargeIn {
				s.mu.Lock()
				s.secondResponsePending = true
				s.mu.Unlock()
			} else {
				s.emitThirdResponse()
			}
		}
	case messages.StreamTypeResponseCreate:
		// A requested continuation completes the first scheduled turn. The
		// second scheduled input is not committed until this response reaches
		// its terminal MESSAGE.END.
		s.mu.Lock()
		bargeIn := s.bargeIn
		toolResultAccepted := s.toolResultAccepted
		if bargeIn && !toolResultAccepted {
			s.secondResponsePending = true
			s.continuationRequested = true
			s.mu.Unlock()
			return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
		}
		if bargeIn {
			s.continuationRequested = true
		}
		s.mu.Unlock()
		if !bargeIn {
			s.continuationOnce.Do(s.emitSecondResponse)
			break
		}
		s.queueActiveContinuationReady()
	case messages.StreamTypeResponseCancel:
		s.mu.Lock()
		bargeIn := s.bargeIn
		s.mu.Unlock()
		if bargeIn {
			// Keep the provider response active until the client cancellation is
			// observed, then terminate that response without emitting stale audio.
			s.firstResponseEndOnce.Do(s.emitFirstResponseEnd)
		}
	case messages.StreamTypeToolCallEnd:
		value, ok := msg.Value.(*messages.ToolCallEndValue)
		if ok && value != nil && value.ToolCallID == sessionToolBargeInCallID {
			s.resultAcceptedOnce.Do(func() {
				s.mu.Lock()
				s.toolResultAccepted = true
				s.mu.Unlock()
				s.recordLifecycle("result_accepted")
				close(s.resultAccepted)
				s.queueActiveContinuationReady()
			})
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func sessionToolBargeInContextOutcome(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *sessionToolBargeInSession) markFinalResponseObserved() {
	s.finalResponseOnce.Do(func() {
		s.recordLifecycle("final_response_observed")
		close(s.finalResponseObserved)
		s.recv.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("session-tool-barge-in", "final assistant response observed"),
		})
	})
}

// queueActiveContinuationReady places an explicit provider boundary behind
// both sides of the tool lifecycle. The stream observer releases the scripted
// assistant responses only after this marker crosses the session loop, so the
// continuation request has been observed before final-response delivery can
// race scheduled-session close.
func (s *sessionToolBargeInSession) queueActiveContinuationReady() {
	s.mu.Lock()
	ready := s.bargeIn && s.toolResultAccepted && s.continuationRequested && s.secondResponsePending && !s.continuationEmitted
	s.mu.Unlock()
	if !ready {
		return
	}
	s.continuationReadyOnce.Do(func() {
		s.recv.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionUpdated,
			Value: messages.NewSessionUpdatedValue(sessionToolBargeInContinuationReadyID),
		})
	})
}

func (s *sessionToolBargeInSession) emitPendingBargeInContinuation() {
	s.mu.Lock()
	if !s.bargeIn || !s.secondResponsePending || s.continuationEmitted {
		s.mu.Unlock()
		return
	}
	s.continuationEmitted = true
	s.secondResponsePending = false
	s.mu.Unlock()

	s.emitSecondResponse()
	s.emitThirdResponse()
}

func (s *sessionToolBargeInSession) emitFirstResponse() {
	msgs := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(sessionToolBargeInCallID, "slow_tool")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(sessionToolBargeInCallID, "slow_tool", `{"value":"wait"}`)},
	}
	if !s.bargeIn {
		msgs = append(msgs, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}
	msgs = sessionToolBargeInWithResponseID(sessionToolBargeInFirstResponseID, msgs)
	for _, msg := range msgs {
		s.recv.Write(context.Background(), msg)
	}
}

func (s *sessionToolBargeInSession) emitFirstResponseEnd() {
	s.recv.Write(context.Background(), messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: sessionToolBargeInFirstResponseID,
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

func (s *sessionToolBargeInSession) emitSecondResponse() {
	msgs := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("follow-on response")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{3, 0, 4, 0})},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	msgs = sessionToolBargeInWithResponseID(sessionToolBargeInContinuationResponseID, msgs)
	for _, msg := range msgs {
		s.recv.Write(context.Background(), msg)
	}
}

func (s *sessionToolBargeInSession) emitThirdResponse() {
	for _, msg := range sessionToolBargeInWithResponseID(sessionToolBargeInFinalResponseID, []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(sessionToolBargeInFinalResponseText)},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{5, 0, 6, 0})},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}) {
		s.recv.Write(context.Background(), msg)
	}
}

func sessionToolBargeInWithResponseID(responseID string, msgs []messages.StreamMessage) []messages.StreamMessage {
	for index := range msgs {
		msgs[index].ResponseID = responseID
	}
	return msgs
}

func (s *sessionToolBargeInSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *sessionToolBargeInSession) Done() <-chan struct{} { return s.done }

func (s *sessionToolBargeInSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *sessionToolBargeInSession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *sessionToolBargeInSession) recordLifecycle(event string) {
	s.mu.Lock()
	s.lifecycle = append(s.lifecycle, event)
	s.mu.Unlock()
}

func (s *sessionToolBargeInSession) lifecycleSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lifecycle...)
}

type sessionToolBargeInInferencer struct {
	ready   chan struct{}
	bargeIn bool
	mu      sync.Mutex
	session *sessionToolBargeInSession
}

func newSessionToolBargeInInferencer() *sessionToolBargeInInferencer {
	return &sessionToolBargeInInferencer{ready: make(chan struct{})}
}

func newActiveSessionToolBargeInInferencer() *sessionToolBargeInInferencer {
	return &sessionToolBargeInInferencer{ready: make(chan struct{}), bargeIn: true}
}

func (i *sessionToolBargeInInferencer) ConnectSession(context.Context) (messages.Session, error) {
	session := newSessionToolBargeInSessionWithMode(i.bargeIn)
	session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("session-tool-barge-in", "test"),
	})
	session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("session-tool-barge-in"),
	})
	i.mu.Lock()
	i.session = session
	i.mu.Unlock()
	close(i.ready)
	return session, nil
}

func (i *sessionToolBargeInInferencer) connectedSession() *sessionToolBargeInSession {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.session
}

type sessionToolBargeInExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	calls []messages.ToolCall
}

func newSessionToolBargeInExecutor() *sessionToolBargeInExecutor {
	return &sessionToolBargeInExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *sessionToolBargeInExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "slow-result"}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func (e *sessionToolBargeInExecutor) callsSnapshot() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}

func waitSessionToolBargeInSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(sessionLifecycleSafetyTimeout):
		t.Fatalf("timed out waiting for %s after %s", name, sessionLifecycleSafetyTimeout)
	}
}

// TestSessionCommand_FollowOnToolCallWaitsForResultBeforeClientClose drives
// the real agent session command with two scheduled audio turns. The first
// provider response requests a deliberately blocked tool; its continuation is
// released before the second scheduled turn can commit. A stream-observer
// barrier makes the continuation boundary explicit, while the command must
// keep the provider session open until the correlated result is accepted.
func TestSessionCommand_FollowOnToolCallWaitsForResultBeforeClientClose(t *testing.T) {
	inferencer := newSessionToolBargeInInferencer()
	executor := newSessionToolBargeInExecutor()

	secondAssistantResponseObserved := make(chan struct{})
	releaseSecondAssistantObserver := make(chan struct{})
	localSessionCloseObserved := make(chan struct{})
	var localSessionCloseOnce sync.Once
	var releaseObserverOnce sync.Once
	var observerOnce sync.Once
	var assistantResponseCount int
	var localSessionCloseCount int
	var observerMu sync.Mutex

	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		executor,
		&mockInferencer{response: "stateless inferencer should not be called"},
		inferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	defer releaseObserverOnce.Do(func() { close(releaseSecondAssistantObserver) })
	var session *sessionToolBargeInSession
	agentCLI.SetSessionStreamObserver(func(msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeSessionClose {
			if session != nil {
				session.recordLifecycle("client_close")
			}
			observerMu.Lock()
			localSessionCloseCount++
			observerMu.Unlock()
			localSessionCloseOnce.Do(func() { close(localSessionCloseObserved) })
			return
		}
		if msg.Type != messages.StreamTypeMessageEnd || msg.Role != messages.RoleAssistant {
			return
		}
		observerMu.Lock()
		assistantResponseCount++
		response := assistantResponseCount
		observerMu.Unlock()
		if response == 2 {
			observerOnce.Do(func() { close(secondAssistantResponseObserved) })
			<-releaseSecondAssistantObserver
		}
	})

	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
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
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn2.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- rootCmd.ExecuteContext(ctx) }()

	waitSessionToolBargeInSignal(t, inferencer.ready, "session connection")
	session = inferencer.connectedSession()
	if session == nil {
		t.Fatal("connected session was not retained")
	}
	waitSessionToolBargeInSignal(t, executor.started, "slow tool executor to start")
	close(executor.release)
	waitSessionToolBargeInSignal(t, session.resultAccepted, "correlated tool result acceptance")
	waitSessionToolBargeInSignal(t, secondAssistantResponseObserved, "second assistant response.done")
	select {
	case <-session.secondAudioSent:
		t.Fatal("second scheduled audio was sent before the first continuation boundary was released")
	default:
	}

	if got := len(executor.callsSnapshot()); got != 1 {
		t.Fatalf("slow tool executor received %d calls, want exactly one", got)
	}
	if call := executor.callsSnapshot()[0]; call.ID != sessionToolBargeInCallID || call.Name != "slow_tool" {
		t.Fatalf("slow tool call = %#v, want ID %q and name slow_tool", call, sessionToolBargeInCallID)
	}
	select {
	case <-localSessionCloseObserved:
		t.Fatal("client close was sent while the follow-on response observer was still held")
	default:
	}

	// Let the session runner finish observing the first grounded continuation.
	// The next scheduled audio turn is eligible only after this observer barrier
	// is released.
	releaseObserverOnce.Do(func() { close(releaseSecondAssistantObserver) })
	waitSessionToolBargeInSignal(t, session.secondAudioSent, "second scheduled audio dispatch")
	waitSessionToolBargeInSignal(t, localSessionCloseObserved, "client close after accepted tool continuation")

	select {
	case err := <-runErr:
		assertExpectedSemanticLiveRunResult(t, err)
	case <-time.After(sessionLifecycleSafetyTimeout):
		t.Fatalf("session command did not finish after client close within %s", sessionLifecycleSafetyTimeout)
	}

	var resultCount int
	for _, msg := range session.sentSnapshot() {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			value, ok := msg.Value.(*messages.ToolCallEndValue)
			if ok && value != nil && value.ToolCallID == sessionToolBargeInCallID {
				resultCount++
			}
		}
	}
	if resultCount != 1 {
		t.Fatalf("provider received %d correlated tool results, want exactly one", resultCount)
	}
	for _, msg := range session.sentSnapshot() {
		if msg.Type == messages.StreamTypeResponseCancel {
			t.Fatal("completion-gated scheduled tool continuation emitted RESPONSE.CANCEL")
		}
	}
	observerMu.Lock()
	closeCount := localSessionCloseCount
	observerMu.Unlock()
	if closeCount != 1 {
		t.Fatalf("stream observer saw %d client closes, want exactly one", closeCount)
	}
	gotLifecycle := session.lifecycleSnapshot()
	if len(gotLifecycle) != 3 || gotLifecycle[0] != "result_accepted" || gotLifecycle[1] != "second_audio_sent" || gotLifecycle[2] != "client_close" {
		t.Fatalf("session lifecycle order = %v, want [result_accepted second_audio_sent client_close]", gotLifecycle)
	}
}

func TestSessionCommand_ActiveScheduledAudioPreservesToolResultLifecycle(t *testing.T) {
	inferencer := newActiveSessionToolBargeInInferencer()
	executor := newSessionToolBargeInExecutor()
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		executor,
		&mockInferencer{response: "stateless inferencer should not be called"},
		inferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	var finalResponseTextObserved bool
	var traceMu sync.Mutex
	var trace []string
	agentCLI.SetSessionStreamObserver(func(msg messages.StreamMessage) {
		traceMu.Lock()
		trace = append(trace, fmt.Sprintf("%s role=%s response=%q", msg.Type, msg.Role, msg.ResponseID))
		traceMu.Unlock()
		if msg.Type == messages.StreamTypeSessionUpdated {
			value, ok := msg.Value.(*messages.SessionUpdatedValue)
			if ok && value != nil && value.SessionID == sessionToolBargeInContinuationReadyID {
				if session := inferencer.connectedSession(); session != nil {
					session.emitPendingBargeInContinuation()
				}
			}
			return
		}
		if msg.Role == messages.RoleAssistant && msg.Type == messages.StreamTypeTextDelta {
			value, ok := msg.Value.(*messages.TextDeltaValue)
			if ok && value != nil && msg.ResponseID == sessionToolBargeInFinalResponseID && value.Content == sessionToolBargeInFinalResponseText {
				finalResponseTextObserved = true
			}
		}
		if msg.Role == messages.RoleAssistant && msg.Type == messages.StreamTypeMessageEnd && msg.ResponseID == sessionToolBargeInFinalResponseID && finalResponseTextObserved {
			if session := inferencer.connectedSession(); session != nil {
				session.markFinalResponseObserved()
			}
		}
		if msg.Type == messages.StreamTypeSessionClose {
			if session := inferencer.connectedSession(); session != nil {
				session.recordLifecycle("client_close")
			}
		}
	})

	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", t.TempDir(),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn2.wav"),
		"--audio-in-turn-barge",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- rootCmd.ExecuteContext(ctx) }()

	waitSessionToolBargeInSignal(t, inferencer.ready, "active scheduled session connection")
	session := inferencer.connectedSession()
	if session == nil {
		t.Fatal("active scheduled connected session was not retained")
	}
	waitSessionToolBargeInSignal(t, executor.started, "active scheduled slow tool executor to start")
	close(executor.release)
	waitSessionToolBargeInSignal(t, session.resultAccepted, "active scheduled correlated tool result acceptance")
	waitSessionToolBargeInSignal(t, session.secondAudioSent, "active scheduled second audio dispatch")
	waitSessionToolBargeInSignal(t, session.finalResponseObserved, "active scheduled final assistant response completion")

	select {
	case err := <-runErr:
		assertExpectedSemanticLiveRunResult(t, err)
	case <-ctx.Done():
		t.Fatalf("active scheduled tool command did not finish: %v", ctx.Err())
	}

	resultCount, cancelCount := 0, 0
	for _, msg := range session.sentSnapshot() {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			value, ok := msg.Value.(*messages.ToolCallEndValue)
			if ok && value != nil && value.ToolCallID == sessionToolBargeInCallID {
				resultCount++
			}
		case messages.StreamTypeResponseCancel:
			cancelCount++
		}
	}
	if resultCount != 1 {
		t.Fatalf("active scheduled provider received %d correlated tool results, want exactly one", resultCount)
	}
	if cancelCount != 1 {
		t.Fatalf("active scheduled provider received %d response cancellations, want exactly one", cancelCount)
	}
	gotLifecycle := session.lifecycleSnapshot()
	if len(gotLifecycle) != 4 || gotLifecycle[0] != "second_audio_sent" || gotLifecycle[1] != "result_accepted" || gotLifecycle[2] != "final_response_observed" || gotLifecycle[3] != "client_close" {
		t.Fatalf("active scheduled session lifecycle order = %v, want [second_audio_sent result_accepted final_response_observed client_close]", gotLifecycle)
	}
}

var _ messages.SessionInferencer = (*sessionToolBargeInInferencer)(nil)
var _ messages.ToolExecutor = (*sessionToolBargeInExecutor)(nil)
