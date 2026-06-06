package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// dispatchMockInferencer is a test double that returns canned responses.
// InferStream returns nil so the model runner falls back to Infer.
type dispatchMockInferencer struct {
	response string
	err      error
}

func (m *dispatchMockInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	if m.err != nil {
		return messages.InferenceResult{}, m.err
	}
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, m.response),
	}, nil
}

func (m *dispatchMockInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, nil // triggers non-streaming fallback in model runner
}

// sequentialMockInferencer cycles through a list of responses in order.
type sequentialMockInferencer struct {
	responses []string
	idx       int
}

func (m *sequentialMockInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	resp := m.responses[m.idx%len(m.responses)]
	m.idx++
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, resp),
	}, nil
}

func (m *sequentialMockInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, nil
}

func TestDispatchAgentTool_SuccessfulDispatch(t *testing.T) {
	inf := &dispatchMockInferencer{response: "task completed successfully"}
	registry := NewToolRegistry()
	tool := NewDispatchAgentTool(inf, registry)

	msgs, err := tool.Execute(context.Background(), map[string]any{
		"system_prompt": "You are a helpful assistant.",
		"task":          "Do the task.",
	})
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if got := msgs[0].TextContent(); got != "task completed successfully" {
		t.Errorf("expected 'task completed successfully', got %q", got)
	}
}

func TestDispatchAgentTool_ChildErrorHandling(t *testing.T) {
	inf := &dispatchMockInferencer{err: errors.New("inference failed")}
	registry := NewToolRegistry()
	tool := NewDispatchAgentTool(inf, registry)

	// Child errors must be returned as tool messages (not fatal errors) so the parent agent can handle them.
	msgs, err := tool.Execute(context.Background(), map[string]any{
		"system_prompt": "You are a helpful assistant.",
		"task":          "Do the task.",
	})
	if err != nil {
		t.Fatalf("expected error as tool message, got fatal error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(msgs))
	}
	text := msgs[0].TextContent()
	if text == "" {
		t.Error("expected non-empty error message")
	}
}

func TestDispatchAgentTool_ToolFiltering(t *testing.T) {
	inf := &dispatchMockInferencer{response: "done"}
	registry := NewToolRegistry() // contains exec, sleep, read_file, etc.
	tool := NewDispatchAgentTool(inf, registry)

	// Request only the "sleep" tool.
	childRegistry := tool.buildChildRegistry([]string{"sleep"})

	if _, ok := childRegistry.Get("sleep"); !ok {
		t.Error("sleep tool should be in child registry")
	}
	if _, ok := childRegistry.Get("exec"); ok {
		t.Error("exec tool should NOT be in child registry when filtering to sleep only")
	}
	if childRegistry.Count() != 1 {
		t.Errorf("expected 1 tool in filtered child registry, got %d", childRegistry.Count())
	}
}

func TestDispatchAgentTool_ToolFilteringAllTools(t *testing.T) {
	inf := &dispatchMockInferencer{response: "done"}
	registry := NewToolRegistry()
	tool := NewDispatchAgentTool(inf, registry)

	// Empty tool names → all parent tools included.
	childRegistry := tool.buildChildRegistry(nil)
	if childRegistry.Count() != registry.Count() {
		t.Errorf("expected child registry to have same count as parent (%d), got %d",
			registry.Count(), childRegistry.Count())
	}
}

func TestDispatchAgentTool_ChildRunsIndependently(t *testing.T) {
	// Two separate Execute calls should produce independent results with no shared history.
	inf := &sequentialMockInferencer{responses: []string{"first response", "second response"}}
	registry := NewToolRegistry()
	tool := NewDispatchAgentTool(inf, registry)

	msgs1, err := tool.Execute(context.Background(), map[string]any{
		"system_prompt": "You are agent 1.",
		"task":          "Task 1.",
	})
	if err != nil {
		t.Fatalf("call 1 unexpected error: %v", err)
	}
	msgs2, err := tool.Execute(context.Background(), map[string]any{
		"system_prompt": "You are agent 2.",
		"task":          "Task 2.",
	})
	if err != nil {
		t.Fatalf("call 2 unexpected error: %v", err)
	}

	text1 := msgs1[0].TextContent()
	text2 := msgs2[0].TextContent()
	if text1 == text2 {
		t.Errorf("expected independent results, got same text %q for both calls", text1)
	}
	if text1 != "first response" {
		t.Errorf("expected 'first response', got %q", text1)
	}
	if text2 != "second response" {
		t.Errorf("expected 'second response', got %q", text2)
	}
}

func TestDispatchAgentTool_MissingSystemPrompt(t *testing.T) {
	inf := &dispatchMockInferencer{response: "done"}
	registry := NewToolRegistry()
	tool := NewDispatchAgentTool(inf, registry)

	msgs, err := tool.Execute(context.Background(), map[string]any{
		"task": "Do the task.",
	})
	if err != nil {
		t.Fatalf("expected error as tool message, got fatal error: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least one error message")
	}
	if msgs[0].TextContent() == "" {
		t.Error("expected non-empty error message for missing system_prompt")
	}
}

func TestDispatchAgentTool_MissingTask(t *testing.T) {
	inf := &dispatchMockInferencer{response: "done"}
	registry := NewToolRegistry()
	tool := NewDispatchAgentTool(inf, registry)

	msgs, err := tool.Execute(context.Background(), map[string]any{
		"system_prompt": "You are helpful.",
	})
	if err != nil {
		t.Fatalf("expected error as tool message, got fatal error: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least one error message")
	}
	if msgs[0].TextContent() == "" {
		t.Error("expected non-empty error message for missing task")
	}
}

func TestDispatchAgentTool_ImplementsAsyncTool(t *testing.T) {
	inf := &dispatchMockInferencer{response: "done"}
	registry := NewToolRegistry()
	tool := NewDispatchAgentTool(inf, registry)

	// Verify SetCallback is accepted without panic.
	var called bool
	tool.SetCallback(func(_ context.Context, _ []messages.Message, _ error) {
		called = true
	})
	_ = called

	// Compile-time check is already enforced by var _ AsyncTool = (*DispatchAgentTool)(nil).
	var _ AsyncTool = tool
}

func TestDispatchAgentTool_RegistrableInRegistry(t *testing.T) {
	inf := &dispatchMockInferencer{response: "done"}
	registry := NewToolRegistry()
	registry.Register(NewDispatchAgentTool(inf, registry))

	tool, ok := registry.Get("dispatch_agent")
	if !ok {
		t.Fatal("dispatch_agent should be retrievable after registration")
	}
	if tool.Name() != "dispatch_agent" {
		t.Errorf("expected name 'dispatch_agent', got %q", tool.Name())
	}
}

func TestParseToolNames_SliceString(t *testing.T) {
	names := parseToolNames([]string{"exec", "sleep"})
	if len(names) != 2 || names[0] != "exec" || names[1] != "sleep" {
		t.Errorf("unexpected result: %v", names)
	}
}

func TestParseToolNames_SliceAny(t *testing.T) {
	names := parseToolNames([]any{"exec", "sleep"})
	if len(names) != 2 || names[0] != "exec" || names[1] != "sleep" {
		t.Errorf("unexpected result: %v", names)
	}
}

func TestParseToolNames_Nil(t *testing.T) {
	names := parseToolNames(nil)
	if names != nil {
		t.Errorf("expected nil, got %v", names)
	}
}
