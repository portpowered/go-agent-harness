package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestExecuteResultFinalText_EmptySuccess(t *testing.T) {
	result := ExecuteResult{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, ""),
		},
	}

	final := result.FinalText()
	if final.Status != FinalTextEmptySuccess {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextEmptySuccess)
	}
	if final.Text != "" {
		t.Fatalf("FinalText text = %q, want empty", final.Text)
	}
	if final.Err != nil {
		t.Fatalf("FinalText err = %v, want nil", final.Err)
	}
}

func TestExecuteResultFinalText_NoFinalMessage(t *testing.T) {
	result := ExecuteResult{
		Messages: []messages.Message{
			{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "lookup"}}},
		},
	}

	final := result.FinalText()
	if final.Status != FinalTextNoFinalMessage {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextNoFinalMessage)
	}
}

func TestExecuteResultFinalText_CanceledWithPartialText(t *testing.T) {
	result := ExecuteResult{
		Deltas: []messages.StreamMessage{
			{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")},
		},
		Err: context.Canceled,
	}

	final := result.FinalText()
	if final.Status != FinalTextCanceled {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextCanceled)
	}
	if !errors.Is(final.Err, context.Canceled) {
		t.Fatalf("FinalText err = %v, want context.Canceled", final.Err)
	}
	if !final.Partial {
		t.Fatal("FinalText Partial = false, want true")
	}
	if final.Text != "partial" {
		t.Fatalf("FinalText text = %q, want partial", final.Text)
	}
}

func TestExecuteResultFinalText_TerminalFailure(t *testing.T) {
	err := errors.New("inference failed")
	result := ExecuteResult{Err: err}

	final := result.FinalText()
	if final.Status != FinalTextFailed {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextFailed)
	}
	if !errors.Is(final.Err, err) {
		t.Fatalf("FinalText err = %v, want %v", final.Err, err)
	}
	if final.Partial {
		t.Fatal("FinalText Partial = true, want false")
	}
}

func TestExecuteResultFinalText_ReportsTerminalSourceFromDeltas(t *testing.T) {
	result := ExecuteResult{
		Deltas: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageEnd, Value: messages.NewSynthesizedMessageEndValue(messages.TokenUsage{})},
		},
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, "from fallback"),
		},
	}

	final := result.FinalText()
	if final.Status != FinalTextSuccess {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextSuccess)
	}
	if final.TerminalSource != messages.TerminalSourceLoopSynthesized {
		t.Fatalf("FinalText terminal source = %q, want %q", final.TerminalSource, messages.TerminalSourceLoopSynthesized)
	}
}

func TestStreamOutcome_Drained(t *testing.T) {
	ch := make(chan streamEvent, 1)
	ch <- streamEvent{event: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("ok")}}
	close(ch)

	stream := newChanStream(ch)
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want first event")
	}
	if stream.Response().Type != messages.StreamTypeTextDelta {
		t.Fatalf("Response type = %q, want %q", stream.Response().Type, messages.StreamTypeTextDelta)
	}
	if stream.Outcome().Status != StreamOpen {
		t.Fatalf("Outcome before drain = %q, want %q", stream.Outcome().Status, StreamOpen)
	}
	if stream.HasNext() {
		t.Fatal("HasNext = true after closed channel")
	}

	outcome := stream.Outcome()
	if outcome.Status != StreamDrained {
		t.Fatalf("Outcome status = %q, want %q", outcome.Status, StreamDrained)
	}
	if outcome.Err != nil {
		t.Fatalf("Outcome err = %v, want nil", outcome.Err)
	}
	if outcome.Partial {
		t.Fatal("Outcome Partial = true, want false on clean drain")
	}
}

func TestStreamOutcome_ReportsProviderTerminalSource(t *testing.T) {
	ch := make(chan streamEvent, 1)
	ch <- streamEvent{event: messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}}
	close(ch)

	stream := newChanStream(ch)
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want MESSAGE.END event")
	}
	if stream.HasNext() {
		t.Fatal("HasNext = true after closed channel")
	}

	outcome := stream.Outcome()
	if outcome.Status != StreamDrained {
		t.Fatalf("Outcome status = %q, want %q", outcome.Status, StreamDrained)
	}
	if outcome.TerminalSource != messages.TerminalSourceProvider {
		t.Fatalf("Outcome terminal source = %q, want %q", outcome.TerminalSource, messages.TerminalSourceProvider)
	}
}

func TestStreamOutcome_ReportsLoopSynthesizedTerminalSource(t *testing.T) {
	ch := make(chan streamEvent, 1)
	ch <- streamEvent{event: messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewSynthesizedMessageEndValue(messages.TokenUsage{})}}
	close(ch)

	stream := newChanStream(ch)
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want MESSAGE.END event")
	}
	if stream.HasNext() {
		t.Fatal("HasNext = true after closed channel")
	}

	outcome := stream.Outcome()
	if outcome.TerminalSource != messages.TerminalSourceLoopSynthesized {
		t.Fatalf("Outcome terminal source = %q, want %q", outcome.TerminalSource, messages.TerminalSourceLoopSynthesized)
	}
}

func TestStreamOutcome_Closed(t *testing.T) {
	ch := make(chan streamEvent)
	stream := newChanStream(ch)

	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if stream.HasNext() {
		t.Fatal("HasNext = true after Close")
	}

	outcome := stream.Outcome()
	if outcome.Status != StreamClosed {
		t.Fatalf("Outcome status = %q, want %q", outcome.Status, StreamClosed)
	}
	if outcome.Err != nil {
		t.Fatalf("Outcome err = %v, want nil", outcome.Err)
	}
	close(ch)
}

func TestStreamOutcome_FailedWithPartialOutput(t *testing.T) {
	err := errors.New("provider failed")
	ch := make(chan streamEvent, 2)
	ch <- streamEvent{event: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")}}
	ch <- streamEvent{
		event: messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(err.Error())},
		err:   err,
	}
	close(ch)

	stream := newChanStream(ch)
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want partial event")
	}
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want error event")
	}
	if stream.HasNext() {
		t.Fatal("HasNext = true after failed stream closed")
	}

	outcome := stream.Outcome()
	if outcome.Status != StreamFailed {
		t.Fatalf("Outcome status = %q, want %q", outcome.Status, StreamFailed)
	}
	if !errors.Is(outcome.Err, err) {
		t.Fatalf("Outcome err = %v, want %v", outcome.Err, err)
	}
	if !outcome.Partial {
		t.Fatal("Outcome Partial = false, want true")
	}
}

func TestStreamOutcome_Canceled(t *testing.T) {
	ch := make(chan streamEvent, 1)
	ch <- streamEvent{
		event: messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(context.Canceled.Error())},
		err:   context.Canceled,
	}
	close(ch)

	stream := newChanStream(ch)
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want cancellation event")
	}
	if stream.HasNext() {
		t.Fatal("HasNext = true after canceled stream closed")
	}

	outcome := stream.Outcome()
	if outcome.Status != StreamCanceled {
		t.Fatalf("Outcome status = %q, want %q", outcome.Status, StreamCanceled)
	}
	if !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("Outcome err = %v, want context.Canceled", outcome.Err)
	}
	if outcome.Partial {
		t.Fatal("Outcome Partial = true, want false without delivered output")
	}
}

func TestStreamOutcome_CanceledWithPartialOutputPreservesTerminalMetadata(t *testing.T) {
	errValue := messages.NewErrorValueWithTerminal(
		context.Canceled.Error(),
		"cancellation",
		messages.TerminalReasonCancellation,
		messages.TerminalProvenanceLoop,
		messages.TerminalOutputPartial,
	)
	errValue.Err = context.Canceled

	ch := make(chan streamEvent, 2)
	ch <- streamEvent{event: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")}}
	ch <- streamEvent{event: messages.StreamMessage{Type: messages.StreamTypeError, Value: errValue}}
	close(ch)

	stream := newChanStream(ch)
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want partial output")
	}
	if stream.Response().Type != messages.StreamTypeTextDelta {
		t.Fatalf("first response type = %q, want %q", stream.Response().Type, messages.StreamTypeTextDelta)
	}
	if !stream.HasNext() {
		t.Fatal("HasNext = false, want cancellation event")
	}
	value, ok := stream.Response().Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("error value type = %T, want *messages.ErrorValue", stream.Response().Value)
	}
	if value.Classification != "cancellation" {
		t.Fatalf("classification = %q, want cancellation", value.Classification)
	}
	if value.TerminalReason != messages.TerminalReasonCancellation {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonCancellation)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputPartial)
	}
	if stream.HasNext() {
		t.Fatal("HasNext = true after canceled stream closed")
	}

	outcome := stream.Outcome()
	if outcome.Status != StreamCanceled {
		t.Fatalf("Outcome status = %q, want %q", outcome.Status, StreamCanceled)
	}
	if !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("Outcome err = %v, want context.Canceled", outcome.Err)
	}
	if !outcome.Partial {
		t.Fatal("Outcome Partial = false, want true")
	}
}
