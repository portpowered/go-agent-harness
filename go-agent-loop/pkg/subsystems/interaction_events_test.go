package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

func newInteractionEventsTestState() *state.LoopState {
	return &state.LoopState{
		Outputs: state.OutputBuffers{
			UserInbox:        *messages.NewTypedBuffer[messages.UserRequest](8),
			KernelDeltaInbox: *messages.NewTypedBuffer[messages.KernelDeltaRequest](16),
		},
	}
}

func TestInteractionEvents_TracksStateAndOutputs(t *testing.T) {
	subsystem := NewInteractionEvents(nil)
	ls := newInteractionEventsTestState()
	ls.Inputs.InteractionEvents = []messages.InteractionEvent{
		{InteractionID: "int-1", Sequence: 1, Type: messages.InteractionEventStart, Provider: "fake", Model: "demo"},
		{InteractionID: "int-1", Sequence: 2, Type: messages.InteractionEventTextDelta, TextDelta: "hello"},
		{InteractionID: "int-1", Sequence: 3, Type: messages.InteractionEventToolCallRequest, ToolCall: &messages.ToolCall{ID: "tool-1", Name: "lookup", Arguments: `{"q":"weather"}`}},
		{InteractionID: "int-1", Sequence: 4, Type: messages.InteractionEventFinalMessage, FinalMessage: &messages.Message{
			Role:         messages.RoleAssistant,
			ContentParts: []messages.ContentPart{messages.NewTextPart("done")},
		}},
		{InteractionID: "int-1", Sequence: 5, Type: messages.InteractionEventUsage, Usage: &messages.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}},
		{InteractionID: "int-1", Sequence: 6, Type: messages.InteractionEventEnd},
	}

	if err := subsystem.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ls.Interaction.ActiveInteractionID != "int-1" {
		t.Fatalf("active interaction = %q", ls.Interaction.ActiveInteractionID)
	}
	if ls.Interaction.LatestSequence != 6 {
		t.Fatalf("latest sequence = %d, want 6", ls.Interaction.LatestSequence)
	}
	if ls.Interaction.Provider != "fake" || ls.Interaction.Model != "demo" {
		t.Fatalf("provider/model = %q/%q", ls.Interaction.Provider, ls.Interaction.Model)
	}
	if !ls.Interaction.Completed {
		t.Fatal("expected interaction to be completed")
	}
	if len(ls.Interaction.PendingToolCalls) != 0 {
		t.Fatalf("pending tool calls = %d, want 0 after final message", len(ls.Interaction.PendingToolCalls))
	}
	if ls.Interaction.FinalMessage == nil || ls.Interaction.FinalMessage.TextContent() != "done" {
		t.Fatalf("final message = %#v", ls.Interaction.FinalMessage)
	}
	if ls.Interaction.Usage == nil || ls.Interaction.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v", ls.Interaction.Usage)
	}
	if !ls.Inputs.TerminateLoop {
		t.Fatal("expected terminate loop to be set on interaction end")
	}

	userReq, ok := ls.Outputs.UserInbox.Read()
	if !ok {
		t.Fatal("expected final message to be forwarded to user inbox")
	}
	if got := userReq.Message.TextContent(); got != "done" {
		t.Fatalf("user inbox text = %q, want done", got)
	}

	first, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected text delta")
	}
	if first.Delta.Type != messages.StreamTypeTextDelta {
		t.Fatalf("first kernel delta = %s, want %s", first.Delta.Type, messages.StreamTypeTextDelta)
	}

	second, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected tool call start")
	}
	if second.Delta.Type != messages.StreamTypeToolCallStart {
		t.Fatalf("second kernel delta = %s, want %s", second.Delta.Type, messages.StreamTypeToolCallStart)
	}

	third, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected tool call end")
	}
	if third.Delta.Type != messages.StreamTypeToolCallEnd {
		t.Fatalf("third kernel delta = %s, want %s", third.Delta.Type, messages.StreamTypeToolCallEnd)
	}

	fourth, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected full final message")
	}
	if fourth.Delta.Type != messages.StreamTypeSystemFullMessage {
		t.Fatalf("fourth kernel delta = %s, want %s", fourth.Delta.Type, messages.StreamTypeSystemFullMessage)
	}
}

func TestInteractionEvents_RecordsTerminalErrorAndCancellation(t *testing.T) {
	subsystem := NewInteractionEvents(nil)
	ls := newInteractionEventsTestState()
	ls.Inputs.InteractionEvents = []messages.InteractionEvent{
		{InteractionID: "int-1", Sequence: 1, Type: messages.InteractionEventStart},
		{InteractionID: "int-1", Sequence: 2, Type: messages.InteractionEventCancellation, Cancellation: &messages.InteractionCancellation{Reason: "caller_cancelled", Message: "caller stopped"}},
		{InteractionID: "int-1", Sequence: 3, Type: messages.InteractionEventError, Error: &messages.InteractionError{Code: "provider_timeout", Message: "timed out", Classification: "transport", Retryable: true}},
	}

	if err := subsystem.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ls.Interaction.Cancellation == nil || ls.Interaction.Cancellation.Reason != "caller_cancelled" {
		t.Fatalf("cancellation = %#v", ls.Interaction.Cancellation)
	}
	if ls.Interaction.TerminalError == nil || ls.Interaction.TerminalError.Code != "provider_timeout" {
		t.Fatalf("terminal error = %#v", ls.Interaction.TerminalError)
	}
	if ls.Interaction.TerminalError.Classification != "transport" {
		t.Fatalf("terminal classification = %q, want transport", ls.Interaction.TerminalError.Classification)
	}

	req, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected error delta")
	}
	if req.Delta.Type != messages.StreamTypeError {
		t.Fatalf("error delta type = %s, want %s", req.Delta.Type, messages.StreamTypeError)
	}
	errValue, ok := req.Delta.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("error delta value = %T, want *messages.ErrorValue", req.Delta.Value)
	}
	if errValue.Classification != "transport" {
		t.Fatalf("error delta classification = %q, want transport", errValue.Classification)
	}
}

func TestInteractionEvents_RejectsDuplicateOrOutOfOrderSequence(t *testing.T) {
	subsystem := NewInteractionEvents(nil)
	ls := newInteractionEventsTestState()
	ls.Interaction = messages.InteractionState{
		ActiveInteractionID: "int-1",
		LatestSequence:      3,
	}
	ls.Inputs.InteractionEvents = []messages.InteractionEvent{
		{InteractionID: "int-1", Sequence: 3, Type: messages.InteractionEventTextDelta, TextDelta: "duplicate"},
	}

	if err := subsystem.Execute(context.Background(), ls); err == nil {
		t.Fatal("expected duplicate sequence to fail")
	}
}

func TestInteractionEvents_LoopEndRemainsAfterInteractionOutputs(t *testing.T) {
	events := NewInteractionEvents(nil)
	coordDelta := NewCoordinatorDelta(*messages.NewTypedBuffer[messages.KernelDeltaRequest](16), nil)
	ls := newInteractionEventsTestState()
	ls.Outputs.KernelDeltaInbox = coordDelta.kernelDeltaInbox
	ls.Inputs.InteractionEvents = []messages.InteractionEvent{
		{InteractionID: "int-1", Sequence: 1, Type: messages.InteractionEventStart},
		{InteractionID: "int-1", Sequence: 2, Type: messages.InteractionEventFinalMessage, FinalMessage: &messages.Message{
			Role:         messages.RoleAssistant,
			ContentParts: []messages.ContentPart{messages.NewTextPart("done")},
		}},
		{InteractionID: "int-1", Sequence: 3, Type: messages.InteractionEventEnd},
	}

	if err := events.Execute(context.Background(), ls); err != nil {
		t.Fatalf("interaction subsystem error: %v", err)
	}
	if err := coordDelta.Execute(context.Background(), ls); err != nil {
		t.Fatalf("coordinator delta error: %v", err)
	}

	first, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected full message before loop end")
	}
	if first.Delta.Type != messages.StreamTypeSystemFullMessage {
		t.Fatalf("first delta = %s, want %s", first.Delta.Type, messages.StreamTypeSystemFullMessage)
	}
	second, ok := ls.Outputs.KernelDeltaInbox.Read()
	if !ok {
		t.Fatal("expected loop end")
	}
	if second.Delta.Type != messages.StreamTypeLoopEnd {
		t.Fatalf("second delta = %s, want %s", second.Delta.Type, messages.StreamTypeLoopEnd)
	}
}
