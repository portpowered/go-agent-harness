package subsystems

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// TestPingPongSubsystem_EmitsPong directly tests the PingPong subsystem.
func TestPingPongSubsystem_EmitsPong(t *testing.T) {
	inbox := *messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := NewPingPong(inbox, nil)

	ls := &state.LoopState{
		Mode: state.DuplexSession,
	}
	ls.Outputs.KernelDeltaInbox = inbox
	ls.Inputs.UserControlPlaneMessage = []messages.Message{
		{
			Role: messages.RoleUser,
			ContentParts: []messages.ContentPart{
				messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
			},
		},
	}

	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Read PONG from inbox.
	req, ok := inbox.Read()
	if !ok {
		t.Fatal("expected PONG in kernel delta inbox")
	}
	if req.Delta.Type != messages.StreamTypePong {
		t.Errorf("delta type: got %q, want %q", req.Delta.Type, messages.StreamTypePong)
	}
	if pv, ok := req.Delta.Value.(*messages.PongValue); ok {
		now := time.Now().UnixMilli()
		diff := now - pv.Timestamp
		if diff < 0 || diff > 5000 {
			t.Errorf("PONG timestamp unreasonable: diff=%dms", diff)
		}
	} else {
		t.Error("PONG value is not *PongValue")
	}
}

// TestPingPongSubsystem_NoOpInNonSession verifies ping/pong is a no-op outside DuplexSession.
func TestPingPongSubsystem_NoOpInNonSession(t *testing.T) {
	inbox := *messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := NewPingPong(inbox, nil)

	ls := &state.LoopState{
		Mode: state.ModeAskOnce, // Not session mode.
	}
	ls.Outputs.KernelDeltaInbox = inbox
	ls.Inputs.UserControlPlaneMessage = []messages.Message{
		{
			Role: messages.RoleUser,
			ContentParts: []messages.ContentPart{
				messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
			},
		},
	}

	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Should have NO pong.
	_, ok := inbox.Read()
	if ok {
		t.Error("PONG should not be emitted in non-session mode")
	}
}

// TestPingPongSubsystem_MultiplePings verifies multiple pings produce multiple pongs.
func TestPingPongSubsystem_MultiplePings(t *testing.T) {
	inbox := *messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := NewPingPong(inbox, nil)

	pingMsg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
		},
	}

	ls := &state.LoopState{
		Mode: state.DuplexSession,
	}
	ls.Outputs.KernelDeltaInbox = inbox
	ls.Inputs.UserControlPlaneMessage = []messages.Message{pingMsg, pingMsg, pingMsg}

	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	pongCount := 0
	for {
		_, ok := inbox.Read()
		if !ok {
			break
		}
		pongCount++
	}
	if pongCount != 3 {
		t.Errorf("pong count: got %d, want 3", pongCount)
	}
}
