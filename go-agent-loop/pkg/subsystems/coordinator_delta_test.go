package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/state"
)

// newCoordinatorDeltaTestState returns a LoopState with KernelDeltaInbox initialised.
func newCoordinatorDeltaTestState() *state.LoopState {
	return &state.LoopState{
		Outputs: state.OutputBuffers{
			KernelDeltaInbox: *messages.NewTypedBuffer[messages.KernelDeltaRequest](16),
		},
	}
}

// --- Delta forwarding to kernel inbox ---

func TestCoordinatorDelta_ForwardsModelDeltas(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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

// --- No forwarding when no deltas ---

func TestCoordinatorDelta_NoInputsNoOutput(t *testing.T) {
	buf := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
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
	cd := NewCoordinatorDelta(*buf, nil)
	if cd.TickGroup() != TickGroupCoordinatorDelta {
		t.Errorf("TickGroup: got %d, want %d", cd.TickGroup(), TickGroupCoordinatorDelta)
	}
	if TickGroupCoordinator >= TickGroupCoordinatorDelta {
		t.Errorf("Coordinator (%d) must run before CoordinatorDelta (%d)",
			TickGroupCoordinator, TickGroupCoordinatorDelta)
	}
}
