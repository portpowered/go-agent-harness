package toolpolicy_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	live "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live"
	toolpolicy "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/toolpolicy"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestTimedToolExecutorUsesSchedulerDeadline(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(600, 0), time.Millisecond)
	tool := blockingTool{started: make(chan struct{})}
	executor := toolpolicy.NewTimedToolExecutor(tool, clock, 9*time.Millisecond)
	result := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-1", Name: "slow"})
		result <- err
	}()
	select {
	case <-tool.started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	clock.AdvanceBy(9 * time.Millisecond)
	select {
	case err := <-result:
		if !errors.Is(err, session.ErrLiveToolExecutionTimeout) {
			t.Fatalf("tool error = %v, want ErrLiveToolExecutionTimeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool deadline")
	}
}

func TestInteractivePolicyToolExecutorCorrelatesClassBudgetExpiry(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(700, 0), time.Millisecond)
	policy := testPolicy(runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout: 3 * time.Millisecond, LongRunningTimeout: 9 * time.Millisecond, AcknowledgementThreshold: time.Millisecond,
	}, "exec")
	tool := blockingTool{started: make(chan struct{})}
	executor := toolpolicy.NewInteractiveToolExecutor(tool, clock, policy, 0)
	result := make(chan struct {
		response messages.ToolCallResponse
		err      error
	}, 1)
	go func() {
		response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-policy-timeout", Name: "exec"})
		result <- struct {
			response messages.ToolCallResponse
			err      error
		}{response, err}
	}()
	select {
	case <-tool.started:
	case <-time.After(time.Second):
		t.Fatal("policy-wrapped tool did not start")
	}
	clock.AdvanceBy(9 * time.Millisecond)
	select {
	case outcome := <-result:
		if outcome.err != nil || outcome.response.ToolCallID != "call-policy-timeout" || outcome.response.Name != "exec" {
			t.Fatalf("policy timeout outcome = %+v, want correlated result", outcome)
		}
		if !strings.Contains(outcome.response.Content, "classification="+runtimeTools.InteractiveToolTimeoutClassification) || !strings.Contains(outcome.response.Content, "after 9ms") {
			t.Fatalf("timeout response content = %q", outcome.response.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy deadline result")
	}
}

func TestInteractivePolicyToolExecutorPreservesUnadvertisedExecutorMarker(t *testing.T) {
	policy := testPolicy(runtimeTools.InteractiveToolPolicySettings{}, "known")
	wrapper := toolpolicy.NewInteractiveToolExecutor(unadvertisedTool{}, platformclock.Real{}, policy, 0)
	replacement, ok := wrapper.(interface{ AllowUnadvertisedTools() bool })
	if !ok || !replacement.AllowUnadvertisedTools() {
		t.Fatalf("policy wrapper marker = (%v, %v), want true", ok, ok && replacement.AllowUnadvertisedTools())
	}
}

func TestLiveInteractivePolicyRoutesAcknowledgement(t *testing.T) {
	provider := newPolicySession()
	for _, message := range []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-tool", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: "response-tool", ToolCallId: "call-policy", Value: messages.NewToolCallStartValue("call-policy", "exec")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "response-tool", ToolCallId: "call-policy", Value: messages.NewToolCallEndValue("call-policy", "exec", `{}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-tool", Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !provider.receive.Write(context.Background(), message) {
			t.Fatal("queue provider interactive-policy messages")
		}
	}
	policy := testPolicy(runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout: 50 * time.Millisecond, LongRunningTimeout: 200 * time.Millisecond, AcknowledgementThreshold: 20 * time.Millisecond,
	}, "exec")
	tool := blockingTool{started: make(chan struct{})}
	service := live.New(live.Dependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return policyInferencer{session: provider}, nil
		},
		Scheduler: platformclock.Real{},
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID:    "interactive-policy-live",
		Capabilities: &session.LiveCapabilities{Executor: tool, Definitions: []messages.ToolDefinition{{Name: "exec"}}, InteractiveToolPolicy: policy},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		handle.Cancel(context.Canceled)
		if err := handle.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("cleanup: %v", err)
		}
	}()
	select {
	case <-tool.started:
	case <-time.After(time.Second):
		t.Fatal("interactive-policy tool did not start")
	}
	deadline := time.After(time.Second)
	for {
		for _, message := range provider.sentSnapshot() {
			value, ok := message.Value.(*messages.ResponseCreateValue)
			if message.Type == messages.StreamTypeResponseCreate && ok && value.IsToolAcknowledgement() {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("interactive-policy acknowledgement was not sent")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestLiveInteractiveToolPolicyRequiresScheduler(t *testing.T) {
	policy := testPolicy(runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout: 5 * time.Second, LongRunningTimeout: 20 * time.Second, AcknowledgementThreshold: 2 * time.Second,
	})
	factoryCalled := false
	service := live.New(live.Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		factoryCalled = true
		return policyInferencer{session: newPolicySession()}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "interactive-policy-without-scheduler",
		Capabilities: &session.LiveCapabilities{
			Executor:              blockingTool{started: make(chan struct{})},
			Definitions:           []messages.ToolDefinition{{Name: "exec"}},
			InteractiveToolPolicy: policy,
		},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); !errors.Is(err, session.ErrLiveSchedulerUnavailable) {
		t.Fatalf("Start = %v, want ErrLiveSchedulerUnavailable", err)
	}
	if factoryCalled {
		t.Fatal("inferencer factory called before scheduler admission rejected the live request")
	}
}

type blockingTool struct{ started chan struct{} }

func (tool blockingTool) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	select {
	case <-tool.started:
	default:
		close(tool.started)
	}
	<-ctx.Done()
	return messages.ToolCallResponse{ToolCallID: call.ID}, ctx.Err()
}

type unadvertisedTool struct{}

func (unadvertisedTool) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "ok"}, nil
}
func (unadvertisedTool) AllowUnadvertisedTools() bool { return true }

type policySnapshot struct {
	settings runtimeTools.InteractiveToolPolicySettings
	long     map[string]bool
}

func testPolicy(settings runtimeTools.InteractiveToolPolicySettings, long ...string) runtimeTools.InteractiveToolPolicy {
	classes := make(map[string]bool, len(long))
	for _, name := range long {
		classes[name] = true
	}
	return policySnapshot{settings: settings, long: classes}
}
func (p policySnapshot) Settings() runtimeTools.InteractiveToolPolicySettings { return p.settings }
func (p policySnapshot) ClassForTool(name string) runtimeTools.InteractiveToolClass {
	if p.long[name] {
		return runtimeTools.InteractiveToolClassBoundedLongRunning
	}
	return runtimeTools.InteractiveToolClassFastRead
}
func (p policySnapshot) TimeoutForTool(name string) time.Duration {
	if p.ClassForTool(name) == runtimeTools.InteractiveToolClassBoundedLongRunning {
		return p.settings.LongRunningTimeout
	}
	return p.settings.FastReadTimeout
}
func (p policySnapshot) Clone() runtimeTools.InteractiveToolPolicy {
	return testPolicy(p.settings, mapKeys(p.long)...)
}
func (p policySnapshot) Validate() error { return nil }
func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

type policyInferencer struct{ session messages.Session }

func (i policyInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type policySession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	close   sync.Once
	mu      sync.Mutex
	sent    []messages.StreamMessage
}

func newPolicySession() *policySession {
	return &policySession{receive: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{})}
}
func (s *policySession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	s.mu.Lock()
	s.sent = append(s.sent, message)
	s.mu.Unlock()
	return true
}
func (s *policySession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *policySession) Done() <-chan struct{}                                  { return s.done }
func (s *policySession) Close() error                                           { s.close.Do(func() { close(s.done) }); return nil }
func (s *policySession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}
