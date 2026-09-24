package engine

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
)

type engineDispatchGateExecutor struct {
	started chan string
	release chan struct{}
}

func (e *engineDispatchGateExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.started <- call.ID
	select {
	case <-e.release:
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "started"}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func TestEngine_ToolDispatchBarrierPrecedesLaterKernelWork(t *testing.T) {
	exec := &engineDispatchGateExecutor{started: make(chan string, 2), release: make(chan struct{})}
	toolRunner := participants.NewToolRunner(exec, 8)
	kernelRunner := participants.NewKernelRunner(nil, 8)
	eng := NewEngine(
		DuplexSession,
		nil,
		[]subsystems.Subsystem{subsystems.NewCoordinator(nil)},
		nil,
		toolRunner,
		nil,
		kernelRunner,
		nil,
	)
	eng.State().LoopState.ToolExecutionAvailable = true
	eng.enforceToolDispatchOrdering = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- toolRunner.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-runnerDone:
		case <-time.After(2 * time.Second):
			t.Error("tool runner did not stop")
		}
	}()

	eng.state.LoopState.Inputs.ModelOutputMessage = []messages.Message{{
		Role: messages.RoleAssistant,
		ToolCalls: []messages.ToolCall{
			{ID: "engine-dispatch-boundary-call-1", Name: "slow"},
			{ID: "engine-dispatch-boundary-call-2", Name: "slow"},
		},
	}}

	worldDone := make(chan error, 1)
	go func() { worldDone <- eng.executeWorldState(ctx, eng.state) }()
	started := map[string]bool{}
	for range 2 {
		select {
		case callID := <-exec.started:
			started[callID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("coordinator did not admit every executor before the barrier")
		}
	}
	for _, callID := range []string{"engine-dispatch-boundary-call-1", "engine-dispatch-boundary-call-2"} {
		if !started[callID] {
			t.Fatalf("executor start missing for %q", callID)
		}
	}
	select {
	case err := <-worldDone:
		if err != nil {
			t.Fatalf("executeWorldState: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine remained behind the executor-start barrier")
	}

	close(exec.release)
}
