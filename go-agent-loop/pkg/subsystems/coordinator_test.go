package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// newCoordinatorTestState returns a LoopState with all output buffers initialised for
// Coordinator tests. Buffer capacity is small (8) since tests are synchronous.
func newCoordinatorTestState() *state.LoopState {
	return &state.LoopState{
		Outputs: state.OutputBuffers{
			ToolInbox:        messages.NewTypedBuffer[messages.ToolBatchRequest](8),
			UserInbox:        messages.NewTypedBuffer[messages.UserRequest](8),
			ModelInbox:       messages.NewTypedBuffer[messages.InferenceRequest](8),
			KernelDeltaInbox: messages.NewTypedBuffer[messages.KernelDeltaRequest](8),
		},
	}
}

// --- Dispatch on new conversation (user output) ---

func TestCoordinator_UserOutputDispatchesInference(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "hello"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// An InferenceRequest should be dispatched to ModelInbox.
	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected InferenceRequest to be dispatched after user output")
	}
	if req.LoopPassID != 1 {
		t.Errorf("InferenceRequest.LoopPassID: got %d, want 1", req.LoopPassID)
	}

	// No tool batch or user request should be dispatched.
	if _, ok := ls.Outputs.ToolInbox.Read(); ok {
		t.Error("no ToolBatchRequest should be dispatched on user output")
	}
	if _, ok := ls.Outputs.UserInbox.Read(); ok {
		t.Error("no UserRequest should be dispatched on user output")
	}
}

func TestCoordinator_UserOutputSendsToKernelDeltaInbox(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "hello"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kd, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected SYSTEM.FULL_MESSAGE to be written to KernelDeltaInbox")
	}
	if kd.Delta.Type != messages.StreamTypeSystemFullMessage {
		t.Errorf("delta type: got %s, want %s", kd.Delta.Type, messages.StreamTypeSystemFullMessage)
	}
	if kd.Source != messages.User {
		t.Errorf("source: got %s, want %s", kd.Source, messages.User)
	}
}

func TestCoordinator_UserOutputResetsModelDeltaTracking(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.History.ConversationDeltaBuffer = make([]messages.StreamMessage, 5)
	ls.History.CurrentModelDeltaCount = 3
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "hello"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ls.History.ModelDeltaStartIndex != 5 {
		t.Errorf("ModelDeltaStartIndex: got %d, want 5", ls.History.ModelDeltaStartIndex)
	}
	if ls.History.CurrentModelDeltaCount != 0 {
		t.Errorf("CurrentModelDeltaCount: got %d, want 0", ls.History.CurrentModelDeltaCount)
	}
}

func TestCoordinator_UserOutputIncreasesPassID(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.History.CurrentPassID = 3
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "test"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ls.History.CurrentPassID != 4 {
		t.Errorf("CurrentPassID: got %d, want 4", ls.History.CurrentPassID)
	}
	req, _ := ls.Outputs.ModelInbox.Read()
	if req.LoopPassID != 4 {
		t.Errorf("InferenceRequest.LoopPassID: got %d, want 4", req.LoopPassID)
	}
}

// --- Dispatch after tool results ---

func TestCoordinator_ToolOutputDispatchesInference(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ToolOutputMessage = []messages.Message{
		{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("result1")}, ToolCallID: "tc1"},
		{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("result2")}, ToolCallID: "tc2"},
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected InferenceRequest after tool output")
	}
	if req.LoopPassID != 1 {
		t.Errorf("InferenceRequest.LoopPassID: got %d, want 1", req.LoopPassID)
	}

	// Should not dispatch tool or user requests.
	if _, ok := ls.Outputs.ToolInbox.Read(); ok {
		t.Error("no ToolBatchRequest should be dispatched on tool output")
	}
	if _, ok := ls.Outputs.UserInbox.Read(); ok {
		t.Error("no UserRequest should be dispatched on tool output")
	}
}

func TestCoordinator_ToolOutputSendsAllToKernelDeltaInbox(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ToolOutputMessage = []messages.Message{
		{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("r1")}, ToolCallID: "tc1"},
		{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("r2")}, ToolCallID: "tc2"},
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both tool messages should be sent to KernelDeltaInbox.
	for i := 0; i < 2; i++ {
		kd, ok := ls.Outputs.KernelDeltaInbox.Read()
		if !ok {
			t.Fatalf("expected KernelDeltaRequest #%d", i)
		}
		if kd.Source != messages.Tool {
			t.Errorf("kd[%d].Source: got %s, want %s", i, kd.Source, messages.Tool)
		}
	}
}

func TestCoordinator_ToolOutputResetsModelDeltaTracking(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.History.ConversationDeltaBuffer = make([]messages.StreamMessage, 10)
	ls.History.CurrentModelDeltaCount = 5
	ls.Inputs.ToolOutputMessage = []messages.Message{
		{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("done")}, ToolCallID: "tc1"},
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ls.History.ModelDeltaStartIndex != 10 {
		t.Errorf("ModelDeltaStartIndex: got %d, want 10", ls.History.ModelDeltaStartIndex)
	}
	if ls.History.CurrentModelDeltaCount != 0 {
		t.Errorf("CurrentModelDeltaCount: got %d, want 0", ls.History.CurrentModelDeltaCount)
	}
}

func TestCoordinator_ToolOutputIncreasesPassID(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.History.CurrentPassID = 2
	ls.Inputs.ToolOutputMessage = []messages.Message{
		{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("done")}, ToolCallID: "tc1"},
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ls.History.CurrentPassID != 3 {
		t.Errorf("CurrentPassID: got %d, want 3", ls.History.CurrentPassID)
	}
}

// --- Tool batch dispatch after tool calls ---

func TestCoordinator_ModelOutputWithToolCallsDispatchesToolBatch(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.ToolExecutionAvailable = true
	ls.Inputs.ModelOutputMessage = []messages.Message{
		{
			Role:         messages.RoleAssistant,
			ContentParts: []messages.ContentPart{messages.NewTextPart("let me call a tool")},
			ToolCalls: []messages.ToolCall{
				{ID: "tc1", Name: "read_file", Arguments: `{"path":"main.go"}`},
				{ID: "tc2", Name: "write_file", Arguments: `{"path":"out.go","content":"x"}`},
			},
		},
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch, ok := ls.Outputs.ToolInbox.Read()
	if !ok {
		t.Fatal("expected ToolBatchRequest after model tool calls")
	}
	if len(batch.Calls) != 2 {
		t.Errorf("ToolBatchRequest.Calls: got %d, want 2", len(batch.Calls))
	}
	if batch.Calls[0].Name != "read_file" {
		t.Errorf("first tool call name: got %s, want read_file", batch.Calls[0].Name)
	}

	// Should not trigger user output or terminate loop.
	if _, ok := ls.Outputs.UserInbox.Read(); ok {
		t.Error("no UserRequest should be dispatched on tool calls")
	}
	if ls.Inputs.TerminateLoop {
		t.Error("TerminateLoop should not be set when model produces tool calls")
	}
}

func TestCoordinator_ModelToolCallIncreasesPassID(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.History.CurrentPassID = 5
	ls.ToolExecutionAvailable = true
	ls.Inputs.ModelOutputMessage = []messages.Message{
		{
			Role:      messages.RoleAssistant,
			ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "tool1", Arguments: "{}"}},
		},
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ls.History.CurrentPassID != 6 {
		t.Errorf("CurrentPassID: got %d, want 6", ls.History.CurrentPassID)
	}
	batch, _ := ls.Outputs.ToolInbox.Read()
	if batch.LoopPassID != 6 {
		t.Errorf("ToolBatchRequest.LoopPassID: got %d, want 6", batch.LoopPassID)
	}
}

func TestCoordinator_ModelToolCallWithoutExecutorDeliversAsFinalResponse(t *testing.T) {
	// Regression for the s2s-v4f-tool-during-audio lane: a realtime provider
	// may issue a function tool call even though the loop has no tool
	// executor wired. Dispatching that call into the idle default executor
	// always fails, and the resulting terminal error raced session shutdown.
	// The coordinator must deliver such messages as final responses instead.
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ModelOutputMessage = []messages.Message{
		{
			Role:      messages.RoleAssistant,
			ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "get_weather", Arguments: `{"city":"Lisbon"}`}},
		},
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := ls.Outputs.ToolInbox.Read(); ok {
		t.Error("ToolBatchRequest must not be dispatched when no tools are configured")
	}
	req, ok := ls.Outputs.UserInbox.Read()
	if !ok {
		t.Fatal("expected UserRequest delivery when tool calls cannot execute")
	}
	if len(req.Message.ToolCalls) != 1 || req.Message.ToolCalls[0].Name != "get_weather" {
		t.Errorf("delivered message = %+v, want the original tool-call message", req.Message)
	}
}

// --- Final response terminates loop ---

func TestCoordinator_ModelOutputFinalResponseTerminatesLoop(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ModelOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleAssistant, "here is the answer"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should send to user inbox.
	ur, ok := ls.Outputs.UserInbox.Read()
	if !ok {
		t.Fatal("expected UserRequest for final model response")
	}
	if ur.Message.TextContent() != "here is the answer" {
		t.Errorf("UserRequest text: got %q, want %q", ur.Message.TextContent(), "here is the answer")
	}

	// TerminateLoop should be set.
	if !ls.Inputs.TerminateLoop {
		t.Error("TerminateLoop should be true for final response")
	}

	// No tool batch should be dispatched.
	if _, ok := ls.Outputs.ToolInbox.Read(); ok {
		t.Error("no ToolBatchRequest should be dispatched for final response")
	}
}

func TestCoordinator_ModelOutputSendsToKernelDeltaInbox(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ModelOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleAssistant, "response"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kd, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected SYSTEM.FULL_MESSAGE for model output")
	}
	if kd.Source != messages.Model {
		t.Errorf("source: got %s, want %s", kd.Source, messages.Model)
	}
	if kd.Delta.Type != messages.StreamTypeSystemFullMessage {
		t.Errorf("delta type: got %s, want %s", kd.Delta.Type, messages.StreamTypeSystemFullMessage)
	}
}

// --- Reasoning-only messages ---

func TestCoordinator_ModelOutputReasoningOnlyDoesNotDispatch(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ModelOutputMessage = []messages.Message{
		messages.NewReasoningMessage(messages.RoleAssistant, "thinking hard..."),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No tool, user, or model dispatch.
	if _, ok := ls.Outputs.ToolInbox.Read(); ok {
		t.Error("no ToolBatchRequest for reasoning-only output")
	}
	if _, ok := ls.Outputs.UserInbox.Read(); ok {
		t.Error("no UserRequest for reasoning-only output")
	}
	if _, ok := ls.Outputs.ModelInbox.Read(); ok {
		t.Error("no InferenceRequest for reasoning-only output")
	}
	if ls.Inputs.TerminateLoop {
		t.Error("TerminateLoop should not be set for reasoning-only output")
	}

	// Full message should still be sent to kernel delta inbox.
	kd, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("reasoning-only message should still be sent to KernelDeltaInbox")
	}
	if kd.Source != messages.Model {
		t.Errorf("source: got %s, want %s", kd.Source, messages.Model)
	}
}

// --- No dispatch when waiting (no inputs) ---

func TestCoordinator_NoInputsNoDispatch(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := ls.Outputs.ModelInbox.Read(); ok {
		t.Error("no InferenceRequest when no inputs")
	}
	if _, ok := ls.Outputs.ToolInbox.Read(); ok {
		t.Error("no ToolBatchRequest when no inputs")
	}
	if _, ok := ls.Outputs.UserInbox.Read(); ok {
		t.Error("no UserRequest when no inputs")
	}
	if _, ok := ls.Outputs.KernelDeltaInbox.Read(); ok {
		t.Error("no KernelDeltaRequest when no inputs")
	}
	if ls.Inputs.TerminateLoop {
		t.Error("TerminateLoop should not be set when no inputs")
	}
}

// --- Empty conversation buffer ---

func TestCoordinator_EmptyConversationBuffer(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	// Conversation buffer is empty (fresh conversation).
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "first message"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected InferenceRequest even with empty conversation buffer")
	}
	// The request should have empty conversation history (hasn't been added to buffer yet by
	// the engine's UpdateWorldHistory which runs after subsystem Execute).
	if len(req.Messages) != 0 {
		t.Errorf("InferenceRequest.Messages: got %d, want 0 (not yet in history)", len(req.Messages))
	}
}

// --- Priority: tool output takes precedence over model and user ---

func TestCoordinator_ToolOutputTakesPriorityOverModelOutput(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ToolOutputMessage = []messages.Message{
		{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("tool result")}, ToolCallID: "tc1"},
	}
	ls.Inputs.ModelOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleAssistant, "model response"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tool output is processed; model output is NOT processed this tick.
	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected InferenceRequest from tool output processing")
	}
	if req.LoopPassID != 1 {
		t.Errorf("InferenceRequest.LoopPassID: got %d, want 1", req.LoopPassID)
	}

	// User inbox should be empty (model output not processed).
	if _, ok := ls.Outputs.UserInbox.Read(); ok {
		t.Error("model output should not be processed when tool output is present")
	}
	if ls.Inputs.TerminateLoop {
		t.Error("TerminateLoop should not be set when tool output takes priority")
	}
}

func TestCoordinator_ModelOutputTakesPriorityOverUserOutput(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ModelOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleAssistant, "final answer"),
	}
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "new user msg"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Model output is processed first.
	ur, ok := ls.Outputs.UserInbox.Read()
	if !ok {
		t.Fatal("expected UserRequest from model final response")
	}
	if ur.Message.TextContent() != "final answer" {
		t.Errorf("UserRequest text: got %q, want %q", ur.Message.TextContent(), "final answer")
	}
	if !ls.Inputs.TerminateLoop {
		t.Error("TerminateLoop should be set for model final response")
	}

	// User output inference should NOT have been dispatched this tick.
	if _, ok := ls.Outputs.ModelInbox.Read(); ok {
		t.Error("user output should not trigger InferenceRequest when model output is present")
	}
}

// --- Inference request includes tools and conversation buffer ---

func TestCoordinator_InferenceRequestIncludesToolsAndHistory(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Tools = []messages.ToolDefinition{
		{Name: "search", Description: "search the web"},
	}
	ls.History.ConversationBuffer = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "previous"),
		messages.NewTextMessage(messages.RoleAssistant, "earlier response"),
	}
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "new question"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected InferenceRequest")
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "search" {
		t.Errorf("InferenceRequest.Tools: got %v, want [search]", req.Tools)
	}
	if len(req.Messages) != 2 {
		t.Errorf("InferenceRequest.Messages: got %d, want 2 (from history)", len(req.Messages))
	}
}

// --- Inference defaults ---

func TestCoordinator_InferenceRequestAppliesDefaults(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	maxTok := 1024
	temp := 0.7
	ls.InferenceDefaults = &messages.InferenceDefaults{
		MaxTokens:   &maxTok,
		Temperature: &temp,
	}
	ls.Inputs.UserOutputMessage = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "test"),
	}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected InferenceRequest")
	}
	if req.MaxTokens == nil || *req.MaxTokens != 1024 {
		t.Errorf("MaxTokens: got %v, want 1024", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("Temperature: got %v, want 0.7", req.Temperature)
	}
}

// --- TickGroup ---

func TestCoordinator_TickGroup(t *testing.T) {
	c := NewCoordinator(nil)
	if c.TickGroup() != TickGroupCoordinator {
		t.Errorf("TickGroup: got %d, want %d", c.TickGroup(), TickGroupCoordinator)
	}
	if TickGroupCoordinator >= TickGroupCoordinatorDelta {
		t.Errorf("Coordinator must run before CoordinatorDelta: %d >= %d",
			TickGroupCoordinator, TickGroupCoordinatorDelta)
	}
}
