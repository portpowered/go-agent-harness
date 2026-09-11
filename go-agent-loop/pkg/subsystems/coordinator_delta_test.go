package subsystems

import (
	"context"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// newCoordinatorDeltaTestState returns a LoopState with KernelDeltaInbox initialised.
func newCoordinatorDeltaTestState() *state.LoopState {
	return &state.LoopState{
		Outputs: state.OutputBuffers{
			KernelDeltaInbox: messages.NewTypedBuffer[messages.KernelDeltaRequest](16),
		},
	}
}

// --- Delta forwarding to kernel inbox ---

func TestCoordinatorDelta_ForwardsModelDeltas(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.ModelInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(" world")},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := 0; i < 2; i++ {
		req, ok := buf.Read()
		if !ok {
			t.Fatalf("expected KernelDeltaRequest #%d for model delta", i)
		}
		if req.Source != messages.Model {
			t.Errorf("delta[%d].Source: got %s, want %s", i, req.Source, messages.Model)
		}
		if req.Delta.Type != messages.StreamTypeTextDelta {
			t.Errorf("delta[%d].Type: got %s, want %s", i, req.Delta.Type, messages.StreamTypeTextDelta)
		}
	}

	if _, ok := buf.Read(); ok {
		t.Error("expected no more deltas after model deltas")
	}
}

func TestCoordinatorDelta_ForwardsToolDeltas(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.ToolInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("tool output")},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected KernelDeltaRequest for tool delta")
	}
	if req.Source != messages.Tool {
		t.Errorf("Source: got %s, want %s", req.Source, messages.Tool)
	}
}

func TestCoordinatorDelta_ForwardsUserDeltas(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.UserInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("user input")},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected KernelDeltaRequest for user delta")
	}
	if req.Source != messages.User {
		t.Errorf("Source: got %s, want %s", req.Source, messages.User)
	}
}

func TestCoordinatorDelta_ForwardsAllSourcesInOrder(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](16)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.ModelInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("model")},
	}
	ls.Inputs.ToolInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("tool")},
	}
	ls.Inputs.UserInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("user")},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Order is: model, tool, user (matches Execute implementation order).
	expected := []messages.ParticipantID{messages.Model, messages.Tool, messages.User}
	for i, want := range expected {
		req, ok := buf.Read()
		if !ok {
			t.Fatalf("expected KernelDeltaRequest #%d", i)
		}
		if req.Source != want {
			t.Errorf("delta[%d].Source: got %s, want %s", i, req.Source, want)
		}
	}
}

// --- LOOP.END emission on termination ---

func TestCoordinatorDelta_EmitsLoopEndOnTermination(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.TerminateLoop = true

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected LOOP.END KernelDeltaRequest")
	}
	if req.Source != messages.System {
		t.Errorf("Source: got %s, want %s", req.Source, messages.System)
	}
	if req.Delta.Type != messages.StreamTypeLoopEnd {
		t.Errorf("Type: got %s, want %s", req.Delta.Type, messages.StreamTypeLoopEnd)
	}
	if _, ok := req.Delta.Value.(*messages.LoopEndValue); !ok {
		t.Errorf("Value: got %T, want *messages.LoopEndValue", req.Delta.Value)
	}
}

func TestCoordinatorDelta_LoopEndAfterDeltas(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.ModelInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("data")},
	}
	ls.Inputs.TerminateLoop = true

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// First should be the model delta.
	first, ok := buf.Read()
	if !ok {
		t.Fatal("expected model delta")
	}
	if first.Source != messages.Model {
		t.Errorf("first.Source: got %s, want %s", first.Source, messages.Model)
	}

	// Second should be LOOP.END.
	second, ok := buf.Read()
	if !ok {
		t.Fatal("expected LOOP.END after model delta")
	}
	if second.Delta.Type != messages.StreamTypeLoopEnd {
		t.Errorf("second.Type: got %s, want %s", second.Delta.Type, messages.StreamTypeLoopEnd)
	}
}

func TestCoordinatorDelta_NoLoopEndWhenNotTerminating(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.TerminateLoop = false
	ls.Inputs.ModelInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("data")},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read the model delta.
	_, ok := buf.Read()
	if !ok {
		t.Fatal("expected model delta")
	}
	// No LOOP.END should follow.
	if _, ok := buf.Read(); ok {
		t.Error("expected no LOOP.END when TerminateLoop is false")
	}
}

func TestCoordinatorDelta_DuplexSessionClientCloseHasTerminalMetadata(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Mode = state.DuplexSession
	ls.SessionID = "session-1"
	ls.Inputs.TerminateLoop = true
	ls.Inputs.UserControlPlaneMessage = []messages.Message{{
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected SESSION.CLOSE KernelDeltaRequest")
	}
	if req.Delta.Type != messages.StreamTypeSessionClose {
		t.Fatalf("delta type = %s, want %s", req.Delta.Type, messages.StreamTypeSessionClose)
	}
	value, ok := req.Delta.Value.(*messages.SessionCloseValue)
	if !ok {
		t.Fatalf("delta value = %T, want *messages.SessionCloseValue", req.Delta.Value)
	}
	if value.TerminalReason != messages.TerminalReasonSessionClose {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonSessionClose)
	}
	if value.Classification != string(messages.TerminalReasonSessionClose) {
		t.Fatalf("classification = %q, want %q", value.Classification, messages.TerminalReasonSessionClose)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputNotApplicable {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNotApplicable)
	}
}

func TestCoordinatorDelta_DuplexSessionStopHasCancellationMetadata(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Mode = state.DuplexSession
	ls.SessionID = "session-1"
	ls.Inputs.TerminateLoop = true
	ls.Inputs.UserControlPlaneMessage = []messages.Message{{
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeStop},
		},
	}}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected SESSION.CLOSE KernelDeltaRequest")
	}
	value, ok := req.Delta.Value.(*messages.SessionCloseValue)
	if !ok {
		t.Fatalf("delta value = %T, want *messages.SessionCloseValue", req.Delta.Value)
	}
	if value.TerminalReason != messages.TerminalReasonCancellation {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonCancellation)
	}
	if value.Classification != string(messages.TerminalReasonCancellation) {
		t.Fatalf("classification = %q, want %q", value.Classification, messages.TerminalReasonCancellation)
	}
	if value.Reason != "stop" {
		t.Fatalf("reason = %q, want stop", value.Reason)
	}
}

// --- No forwarding when no deltas ---

func TestCoordinatorDelta_NoInputsNoOutput(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := buf.Read(); ok {
		t.Error("expected no output when there are no input deltas")
	}
}

// --- Delta metadata preservation (enables downstream stale filtering) ---

func TestCoordinatorDelta_PreservesLoopPassID(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.ModelInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("a"), LoopPassID: 5},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected delta")
	}
	if req.Delta.LoopPassID != 5 {
		t.Errorf("LoopPassID: got %d, want 5", req.Delta.LoopPassID)
	}
}

func TestCoordinatorDelta_PreservesActorID(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.ToolInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("x"), ActorID: messages.Tool},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected delta")
	}
	if req.Delta.ActorID != messages.Tool {
		t.Errorf("ActorID: got %s, want %s", req.Delta.ActorID, messages.Tool)
	}
}

func TestCoordinatorDelta_PreservesGlobalIndex(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(buf, nil)
	ls := newCoordinatorDeltaTestState()
	ls.Inputs.UserInputDelta = []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("z"), GlobalIndex: 42},
	}

	if err := cd.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, ok := buf.Read()
	if !ok {
		t.Fatal("expected delta")
	}
	if req.Delta.GlobalIndex != 42 {
		t.Errorf("GlobalIndex: got %d, want 42", req.Delta.GlobalIndex)
	}
}

// --- TickGroup ---

func TestCoordinatorDelta_TickGroup(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](1)
	cd := NewCoordinatorDelta(buf, nil)
	if cd.TickGroup() != TickGroupCoordinatorDelta {
		t.Errorf("TickGroup: got %d, want %d", cd.TickGroup(), TickGroupCoordinatorDelta)
	}
	if TickGroupCoordinator >= TickGroupCoordinatorDelta {
		t.Errorf("Coordinator (%d) must run before CoordinatorDelta (%d)",
			TickGroupCoordinator, TickGroupCoordinatorDelta)
	}
}
func TestCoordinator_DuplexSessionKeepsOverlappingToolBatchesInOneGeneration(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Mode, ls.ToolExecutionAvailable = state.DuplexSession, true
	for _, callID := range []string{"call-000-alpha", "call-001-alpha"} {
		ls.Inputs.ModelOutputMessage = []messages.Message{{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: callID, Name: "lookup_alpha", Arguments: `{}`}}}}
		if err := c.Execute(context.Background(), ls); err != nil {
			t.Fatalf("Execute %s: %v", callID, err)
		}
		batch, ok := ls.Outputs.ToolInbox.Read()
		if !ok {
			t.Fatalf("expected tool batch for %s", callID)
		}
		if batch.LoopPassID != 1 || ls.History.CurrentPassID != 1 {
			t.Fatalf("generation for %s = batch:%d history:%d, want 1:1", callID, batch.LoopPassID, ls.History.CurrentPassID)
		}
	}
}

func TestCoordinator_DuplexSessionToolContinuationDoesNotRetireSiblingBatch(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Mode, ls.History.CurrentPassID = state.DuplexSession, 4
	ls.Inputs.ToolOutputMessage = []messages.Message{{Role: messages.RoleTool, ToolCallID: "call-000-alpha", ContentParts: []messages.ContentPart{messages.NewTextPart("result")}}}
	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected continuation inference request")
	}
	if req.LoopPassID != 4 || ls.History.CurrentPassID != 4 {
		t.Fatalf("continuation generation = request:%d history:%d, want 4:4", req.LoopPassID, ls.History.CurrentPassID)
	}
}

func TestCoordinator_OverlappingModelResponsesReconstructIndependently(t *testing.T) {
	c := NewCoordinator(nil)
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-zero", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: "response-zero", ToolCallId: "call-zero", Value: messages.NewToolCallStartValue("call-zero", "lookup_zero")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "response-zero", ToolCallId: "call-zero", Value: messages.NewToolCallEndValue("call-zero", "lookup_zero", `{}`)},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-one", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: "response-one", ToolCallId: "call-one", Value: messages.NewToolCallStartValue("call-one", "lookup_one")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "response-one", ToolCallId: "call-one", Value: messages.NewToolCallEndValue("call-one", "lookup_one", `{}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-zero", Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-one", Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	completed, err := c.observeModelResponses(deltas)
	if err != nil {
		t.Fatalf("observeModelResponses: %v", err)
	}
	if !c.modelResponsesOverlap || len(completed) != 2 || len(completed[0].ToolCalls) != 1 || completed[0].ToolCalls[0].ID != "call-zero" || len(completed[1].ToolCalls) != 1 || completed[1].ToolCalls[0].ID != "call-one" {
		t.Fatalf("overlap reconstruction = overlap:%t completed:%#v", c.modelResponsesOverlap, completed)
	}
}

func TestCoordinator_UnscopedToolEventsJoinResponsesOnFinalBoundary(t *testing.T) {
	c := NewCoordinator(nil)
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-zero", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: "call-zero", Value: messages.NewToolCallEndValue("call-zero", "lookup_zero", `{}`)},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-one", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: "call-one", Value: messages.NewToolCallEndValue("call-one", "lookup_one", `{}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-one", Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	completed, err := c.observeModelResponses(deltas)
	if err != nil {
		t.Fatalf("observeModelResponses: %v", err)
	}
	if len(completed) != 1 || len(completed[0].ToolCalls) != 2 || completed[0].ToolCalls[0].ID != "call-zero" || completed[0].ToolCalls[1].ID != "call-one" {
		t.Fatalf("unscoped reconstruction = %#v, want both calls in order", completed)
	}
}

func TestCoordinator_ModelResponseAssemblyIsBounded(t *testing.T) {
	c := NewCoordinator(nil)
	for index := 0; index < maxTrackedModelResponses; index++ {
		if _, err := c.observeModelResponses([]messages.StreamMessage{{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: fmt.Sprintf("response-%d", index), Value: messages.NewMessageStartValue()}}); err != nil {
			t.Fatalf("response %d unexpectedly exceeded assembly bound: %v", index, err)
		}
	}
	if _, err := c.observeModelResponses([]messages.StreamMessage{{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-over-limit", Value: messages.NewMessageStartValue()}}); err == nil {
		t.Fatalf("response assembly accepted more than %d active responses", maxTrackedModelResponses)
	}
}
