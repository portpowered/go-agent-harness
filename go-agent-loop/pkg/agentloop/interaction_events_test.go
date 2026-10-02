package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// signalWriter forwards each turn response written by the loop.
type signalWriter struct{ texts chan string }

func (w signalWriter) Write(p []byte) (int, error) {
	w.texts <- string(p)
	return len(p), nil
}

// TestRunDeliversNormalizedInteractionEvents drives a turn from a gateway's
// normalized interaction events instead of a model inferencer: the final
// message reaches the configured outputs, the interaction end terminates the
// run, and the loop state reports the completed interaction.
func TestRunDeliversNormalizedInteractionEvents(t *testing.T) {
	al, err := New(WithMode(engine.ModeTurnTaking), WithInferencer(&mockInferencer{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	outputs := signalWriter{texts: make(chan string, 1)}
	if err := al.SetOutputs(t.Context(), []Output{{Writer: outputs, Label: "test"}}); err != nil {
		t.Fatalf("SetOutputs: %v", err)
	}
	final := &messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{messages.NewTextPart("done")}}
	events := []messages.InteractionEvent{
		{InteractionID: "int-1", Sequence: 1, Type: messages.InteractionEventStart, Provider: "gateway", Model: "demo"},
		{InteractionID: "int-1", Sequence: 2, Type: messages.InteractionEventTextDelta, TextDelta: "do"},
		{InteractionID: "int-1", Sequence: 3, Type: messages.InteractionEventFinalMessage, FinalMessage: final},
		{InteractionID: "int-1", Sequence: 4, Type: messages.InteractionEventEnd},
	}
	if err := al.SendInteractionEvents(t.Context(), events); err != nil {
		t.Fatalf("SendInteractionEvents: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- al.Run(ctx) }()
	if got := <-outputs.texts; got != "done" {
		t.Fatalf("output = %q, want the interaction's final message", got)
	}
	// The final message reaches the outputs before the End event's tick may
	// have run. The End event is applied in the same tick that publishes
	// LOOP.END (InteractionEvents precedes CoordinatorDelta), so LOOP.END on
	// the public delta stream proves the interaction is complete.
	for {
		delta, err := al.Deltas().ReadContext(ctx)
		if err != nil {
			t.Fatalf("read deltas before LOOP.END: %v", err)
		}
		if delta.Type == messages.StreamTypeLoopEnd {
			break
		}
	}
	// Run is continuous turn-taking: it keeps serving later turns until its
	// caller stops it.
	cancel()
	if err := <-ran; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	state, err := al.GetState(t.Context())
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if interaction := state.Interaction; !interaction.Completed || interaction.ActiveInteractionID != "int-1" || interaction.Provider != "gateway" || interaction.FinalMessage == nil || interaction.FinalMessage.TextContent() != "done" {
		t.Fatalf("interaction state = %+v, want the completed gateway interaction", interaction)
	}
}

// TestPauseAndTodoQueueAreVisibleThroughTheLoop covers the loop's
// out-of-band controls: a pause is reported by GetState, and deferred TODO
// messages are returned first in, first out.
func TestPauseAndTodoQueueAreVisibleThroughTheLoop(t *testing.T) {
	al, err := New(WithInferencer(&mockInferencer{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := al.Pause(t.Context()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if state, err := al.GetState(t.Context()); err != nil || state.RunState != RunState(engine.RunStatePaused) {
		t.Fatalf("state after pause = %+v (%v), want paused", state, err)
	}

	al.EnqueueTodo("first")
	al.EnqueueTodo("second")
	if got := al.TodoQueueLen(); got != 2 {
		t.Fatalf("todo length = %d, want 2", got)
	}
	for _, want := range []string{"first", "second"} {
		if got, ok := al.DequeueTodo(); !ok || got != want {
			t.Fatalf("dequeue = (%q, %t), want %q", got, ok, want)
		}
	}
	if _, ok := al.DequeueTodo(); ok {
		t.Fatal("dequeue from an empty todo queue succeeded")
	}
}

// TestSendAudioInputWithPolicyRequiresSessionMode rejects audio for a
// turn-based loop and admits it to a session's ordered ingress.
func TestSendAudioInputWithPolicyRequiresSessionMode(t *testing.T) {
	turnBased, err := New(WithInferencer(&mockInferencer{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := turnBased.SendAudioInputWithPolicy(context.Background(), []byte{0, 0}, messages.SessionAudioInputPolicyDefault); err == nil {
		t.Fatal("turn-based loop accepted session audio")
	}
	session, err := New(WithMode(engine.DuplexSession), WithSessionInferencer(noopSessionInferencer{}))
	if err != nil {
		t.Fatalf("New session: %v", err)
	}
	if err := session.SendAudioInputWithPolicy(context.Background(), []byte{1, 0}, messages.SessionAudioInputPolicyDefault); err != nil {
		t.Fatalf("session audio: %v", err)
	}
}
