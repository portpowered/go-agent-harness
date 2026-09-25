package wire

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	ackProvider      = "openai"
	ackSlowTool      = "sleep"
	ackFastTool      = "read_file"
	ackCallID        = "call-long"
	ackThreshold     = 2 * time.Second
	ackSignalTimeout = 2 * time.Second
	ackQuietWindow   = 50 * time.Millisecond
)

// ackPolicy classifies ackSlowTool as bounded long-running.
type ackPolicy struct{}

func (ackPolicy) Settings() tools.InteractiveToolPolicySettings {
	return tools.InteractiveToolPolicySettings{FastReadTimeout: time.Second, LongRunningTimeout: 20 * time.Second, AcknowledgementThreshold: ackThreshold}
}

func (ackPolicy) ClassForTool(name string) tools.InteractiveToolClass {
	if name == ackSlowTool {
		return tools.InteractiveToolClassBoundedLongRunning
	}
	return tools.InteractiveToolClassFastRead
}

func (p ackPolicy) TimeoutForTool(name string) time.Duration {
	if p.ClassForTool(name) == tools.InteractiveToolClassBoundedLongRunning {
		return p.Settings().LongRunningTimeout
	}
	return p.Settings().FastReadTimeout
}

func (p ackPolicy) Clone() tools.InteractiveToolPolicy { return p }
func (ackPolicy) Validate() error                      { return nil }

// thresholdScheduler reports when a timer for the acknowledgement threshold
// is armed, so the test advances the fake clock only after the tool runner
// started measuring the pending call.
type thresholdScheduler struct {
	platformclock.Scheduler
	armed chan struct{}
	once  sync.Once
}

func (s *thresholdScheduler) NewTimer(duration time.Duration) platformclock.Timer {
	timer := s.Scheduler.NewTimer(duration)
	if duration == ackThreshold {
		s.once.Do(func() { close(s.armed) })
	}
	return timer
}

// releasedTool blocks until the test releases it.
type releasedTool struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (tool *releasedTool) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	tool.once.Do(func() { close(tool.started) })
	select {
	case <-tool.release:
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "slept"}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

// recordingLiveSession is a provider session double that records every
// client send; the test writes provider messages to its receive buffer.
type recordingLiveSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	close   sync.Once
	mu      sync.Mutex
	sent    []messages.StreamMessage
}

func newRecordingLiveSession() *recordingLiveSession {
	return &recordingLiveSession{receive: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{})}
}

func (s *recordingLiveSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *recordingLiveSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *recordingLiveSession) Done() <-chan struct{} { return s.done }

func (s *recordingLiveSession) Close() error {
	s.close.Do(func() { close(s.done) })
	return nil
}

func (s *recordingLiveSession) hasText(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.sent {
		if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value.Content == text {
			return true
		}
	}
	return false
}

func waitForSentText(t *testing.T, provider *recordingLiveSession, text string) {
	t.Helper()
	waitForCondition(t, "delivery of "+text, func() bool { return provider.hasText(text) })
}

func (s *recordingLiveSession) responseCreates() (acknowledgements, ordinary int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.sent {
		if msg.Type != messages.StreamTypeResponseCreate {
			continue
		}
		if value, ok := msg.Value.(*messages.ResponseCreateValue); ok && value.IsToolAcknowledgement() {
			acknowledgements++
			continue
		}
		ordinary++
	}
	return acknowledgements, ordinary
}

func (s *recordingLiveSession) toolResultSent(callID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.sent {
		if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && msg.Type == messages.StreamTypeToolCallEnd && value.ToolCallID == callID {
			return true
		}
	}
	return false
}

// closeLiveHandle releases a handle at test end; a finished handle closes
// idempotently, so any error is a teardown defect.
func closeLiveHandle(t *testing.T, handle session.LiveHandle) {
	t.Helper()
	if err := handle.Close(); err != nil {
		t.Errorf("close live handle: %v", err)
	}
}

func waitForCondition(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(ackSignalTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(time.Millisecond)
	}
}

func writeProvider(t *testing.T, provider *recordingLiveSession, stream ...messages.StreamMessage) {
	t.Helper()
	for _, msg := range stream {
		if !provider.receive.Write(context.Background(), msg) {
			t.Fatalf("queue provider %s", msg.Type)
		}
	}
}

func assistantResponse(responseID, text string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(text)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
}

func toolCallResponse(callID, name string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-tool", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: "response-tool", ToolCallId: callID, Value: messages.NewToolCallStartValue(callID, name)},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "response-tool", ToolCallId: callID, Value: messages.NewToolCallEndValue(callID, name, `{}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-tool", Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
}

type acknowledgementRun struct {
	provider  *recordingLiveSession
	tool      *releasedTool
	scheduler *thresholdScheduler
	clock     *platformclock.Deterministic
	handle    session.LiveHandle
	done      chan error
}

// startAcknowledgementRun opens a finite live session whose provider calls
// toolName once the opening prompt reaches it.
func startAcknowledgementRun(t *testing.T, providerName, toolName string, policy tools.InteractiveToolPolicy) *acknowledgementRun {
	t.Helper()
	clock := platformclock.NewDeterministic(time.Unix(700, 0), time.Millisecond)
	run := &acknowledgementRun{
		provider:  newRecordingLiveSession(),
		tool:      &releasedTool{started: make(chan struct{}), release: make(chan struct{})},
		scheduler: &thresholdScheduler{Scheduler: clock, armed: make(chan struct{})},
		clock:     clock,
		done:      make(chan error, 1),
	}
	t.Cleanup(func() {
		select {
		case <-run.tool.release:
		default:
			close(run.tool.release)
		}
	})
	writeProvider(t, run.provider, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")})
	service := NewLiveService(LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return sessionInferencer{session: run.provider}, nil
		},
		Clock: clock.Now, Scheduler: run.scheduler,
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "tool-acknowledgement", Provider: providerName, OpeningPrompt: "wait for me", FinishAfterResponse: true,
		Capabilities: &session.LiveCapabilities{
			Executor:    run.tool,
			Definitions: []messages.ToolDefinition{{Name: ackSlowTool}, {Name: ackFastTool}},
			ToolPolicy:  policy,
		},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	t.Cleanup(func() { closeLiveHandle(t, handle) })
	run.handle = handle
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		for range handle.Events() {
		}
	}()
	go func() { run.done <- handle.Wait() }()
	waitForSentText(t, run.provider, "wait for me")
	writeProvider(t, run.provider, toolCallResponse(ackCallID, toolName)...)
	select {
	case <-run.tool.started:
	case <-time.After(ackSignalTimeout):
		t.Fatal("tool did not start")
	}
	return run
}

func (run *acknowledgementRun) assertRunning(t *testing.T, label string) {
	t.Helper()
	select {
	case err := <-run.done:
		t.Fatalf("session finished %s: %v", label, err)
	case <-time.After(ackQuietWindow):
	}
}

// releaseTool completes the pending call and waits for its result to reach
// the provider.
func (run *acknowledgementRun) releaseTool(t *testing.T) {
	t.Helper()
	close(run.tool.release)
	waitForCondition(t, "tool result delivery", func() bool { return run.provider.toolResultSent(ackCallID) })
}

// awaitContinuationRequest waits for the grounded continuation request.
func (run *acknowledgementRun) awaitContinuationRequest(t *testing.T) {
	t.Helper()
	waitForCondition(t, "grounded continuation request", func() bool {
		_, ordinary := run.provider.responseCreates()
		return ordinary > 0
	})
}

// completeContinuation answers the grounded continuation and requires a
// clean finite finish.
func (run *acknowledgementRun) completeContinuation(t *testing.T) {
	t.Helper()
	writeProvider(t, run.provider, assistantResponse("response-final", "all done")...)
	select {
	case err := <-run.done:
		if err != nil {
			t.Fatalf("Wait = %v, want a clean finish after the grounded continuation", err)
		}
	case <-time.After(ackSignalTimeout):
		t.Fatal("session did not finish after the grounded continuation")
	}
}

func (run *acknowledgementRun) awaitAcknowledgementRequest(t *testing.T) {
	t.Helper()
	select {
	case <-run.scheduler.armed:
	case <-time.After(ackSignalTimeout):
		t.Fatal("acknowledgement threshold was not armed on the loop clock")
	}
	run.clock.AdvanceBy(ackThreshold - time.Millisecond)
	time.Sleep(ackQuietWindow)
	if acknowledgements, _ := run.provider.responseCreates(); acknowledgements != 0 {
		t.Fatalf("acknowledgements before the threshold = %d, want 0", acknowledgements)
	}
	run.clock.AdvanceBy(time.Millisecond)
	waitForCondition(t, "acknowledgement request", func() bool {
		acknowledgements, _ := run.provider.responseCreates()
		return acknowledgements == 1
	})
}

// speakAcknowledgement answers the acknowledgement request with an untagged
// provider response; the loop attributes it to the outstanding request.
func (run *acknowledgementRun) speakAcknowledgement(t *testing.T) {
	t.Helper()
	writeProvider(t, run.provider, assistantResponse("response-ack", "one moment")...)
}

func TestLongRunningToolRequestsOneSpokenAcknowledgementAtThreshold(t *testing.T) {
	// The spoken acknowledgement is progress for the pending call: whether it
	// ends before or after the tool result, it must not complete the finite
	// response or stand in for the grounded continuation.
	for name, toolFinishesFirst := range map[string]bool{
		"acknowledgement ends while the tool runs":   false,
		"acknowledgement ends after the tool result": true,
	} {
		t.Run(name, func(t *testing.T) {
			run := startAcknowledgementRun(t, ackProvider, ackSlowTool, ackPolicy{})
			run.awaitAcknowledgementRequest(t)
			if toolFinishesFirst {
				run.releaseTool(t)
				run.speakAcknowledgement(t)
			} else {
				run.speakAcknowledgement(t)
				run.assertRunning(t, "on the acknowledgement response")
				if run.provider.toolResultSent(ackCallID) {
					t.Fatal("tool result was delivered before the tool completed")
				}
				run.clock.AdvanceBy(10 * ackThreshold)
				run.releaseTool(t)
			}
			run.awaitContinuationRequest(t)
			run.assertRunning(t, "before the grounded continuation answered")
			run.completeContinuation(t)
			if acknowledgements, ordinary := run.provider.responseCreates(); acknowledgements != 1 || ordinary != 1 {
				t.Fatalf("response.create sends = %d acknowledgements, %d ordinary; want exactly one of each", acknowledgements, ordinary)
			}
		})
	}
}

func TestFastToolAndPolicylessCapabilityNeverAcknowledge(t *testing.T) {
	for name, tc := range map[string]struct {
		provider string
		tool     string
		policy   tools.InteractiveToolPolicy
	}{
		"fast read tool":  {provider: ackProvider, tool: ackFastTool, policy: ackPolicy{}},
		"no bound policy": {provider: ackProvider, tool: ackSlowTool},
		// Grok terminates the session on any provider error, so a rejected
		// acknowledgement would end the conversation.
		"provider without recoverable rejection": {provider: "grok", tool: ackSlowTool, policy: ackPolicy{}},
	} {
		t.Run(name, func(t *testing.T) {
			run := startAcknowledgementRun(t, tc.provider, tc.tool, tc.policy)
			run.clock.AdvanceBy(10 * ackThreshold)
			time.Sleep(ackQuietWindow)
			if acknowledgements, _ := run.provider.responseCreates(); acknowledgements != 0 {
				t.Fatalf("acknowledgements = %d, want none", acknowledgements)
			}
			run.releaseTool(t)
			run.awaitContinuationRequest(t)
			run.completeContinuation(t)
		})
	}
}
