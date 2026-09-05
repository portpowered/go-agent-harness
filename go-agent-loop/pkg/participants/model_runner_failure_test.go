package participants

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestModelRunnerPublishSessionAudioFailurePreservesCauseAndMetadata(t *testing.T) {
	rootErr := errors.New("provider audio queue closed")
	runner := NewSessionModelRunner(nil, 1, nil)
	runner.currentPassID = 17

	runner.publishSessionAudioFailure(rootErr, false)

	delta, ok := runner.DeltaOutbox.Read()
	if !ok {
		t.Fatal("terminal audio failure was not published")
	}
	if delta.Type != messages.StreamTypeError {
		t.Fatalf("delta type = %q, want %q", delta.Type, messages.StreamTypeError)
	}
	if delta.ActorID != messages.Model {
		t.Fatalf("actor = %q, want %q", delta.ActorID, messages.Model)
	}
	if delta.LoopPassID != 17 {
		t.Fatalf("loop pass = %d, want 17", delta.LoopPassID)
	}
	value, ok := delta.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("delta value = %T, want *messages.ErrorValue", delta.Value)
	}
	if value.Message != rootErr.Error() {
		t.Fatalf("message = %q, want %q", value.Message, rootErr)
	}
	if !errors.Is(value.Err, rootErr) {
		t.Fatalf("error cause = %v, want %v", value.Err, rootErr)
	}
	if value.Classification != sessionAudioSendFailureClassification {
		t.Fatalf("classification = %q, want %q", value.Classification, sessionAudioSendFailureClassification)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNone)
	}
}

func TestModelRunnerPublishSessionAudioFailureEvictsOrdinaryDelta(t *testing.T) {
	runner := NewSessionModelRunner(nil, 1, nil)
	if !runner.DeltaOutbox.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("stale"),
	}) {
		t.Fatal("ordinary delta was not queued")
	}

	runner.publishSessionAudioFailure(errors.New("audio write failed"), true)
	got, ok := runner.DeltaOutbox.Read()
	if !ok || got.Type != messages.StreamTypeError {
		t.Fatalf("queued delta = %#v, ok=%t, want terminal ERROR", got, ok)
	}
	value, ok := got.Value.(*messages.ErrorValue)
	if !ok || value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("terminal value = %#v, want partial output state", got.Value)
	}
}
