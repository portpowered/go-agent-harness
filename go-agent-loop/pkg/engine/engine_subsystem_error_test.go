package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
)

var errSubsystemFailed = errors.New("subsystem failed")

// failingSubsystem fails every tick and records that it ran.
type failingSubsystem struct{ runs int }

func (s *failingSubsystem) Execute(context.Context, *state.LoopState) error {
	s.runs++
	return errSubsystemFailed
}

func (*failingSubsystem) TickGroup() subsystems.TickGroup { return subsystems.TickGroupCoordinator }

// laterSubsystem must not run once an earlier tick group failed.
type laterSubsystem struct{ runs int }

func (s *laterSubsystem) Execute(context.Context, *state.LoopState) error {
	s.runs++
	return nil
}

func (*laterSubsystem) TickGroup() subsystems.TickGroup { return subsystems.TickGroupCoordinatorDelta }

// TestTickReportsSubsystemFailure stops the tick at a failing subsystem,
// names its tick group, skips later groups and does not count the tick.
func TestTickReportsSubsystemFailure(t *testing.T) {
	failing, later := &failingSubsystem{}, &laterSubsystem{}
	modelRunner := participants.NewModelRunner(&noopInferencer{}, 8)
	eng := NewEngine(ModeAskOnce, nil, []subsystems.Subsystem{later, failing}, modelRunner,
		participants.NewToolRunner(&messages.DefaultToolExecutor{}, 8), participants.NewUserRunner(8),
		participants.NewKernelRunner(nil, 8), nil)
	modelRunner.DeltaOutbox.Write(context.Background(), messages.StreamMessage{
		Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue(),
	})

	err := eng.TickOnce(context.Background())
	if !errors.Is(err, errSubsystemFailed) || !strings.Contains(err.Error(), "helper at tick group") {
		t.Fatalf("TickOnce() = %v, want the subsystem failure with its tick group", err)
	}
	if failing.runs != 1 || later.runs != 0 {
		t.Fatalf("runs: failing %d, later %d; want 1 and 0", failing.runs, later.runs)
	}
	if got := eng.TickState().TickCount; got != 0 {
		t.Fatalf("tick count = %d, want the failed tick not counted", got)
	}
}
