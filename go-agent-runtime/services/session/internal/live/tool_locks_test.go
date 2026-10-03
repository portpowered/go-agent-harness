package live

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// longRunningPolicy classifies exec as long-running.
type longRunningPolicy struct{ tools.InteractiveToolPolicy }

func (longRunningPolicy) ClassForTool(name string) tools.InteractiveToolClass {
	if name == "exec" {
		return tools.InteractiveToolClassBoundedLongRunning
	}
	return tools.InteractiveToolClassFastRead
}

// A call waiting for its group's lock (an interrupted voice call, an ended
// delegation) gives up with its context's cause and never runs. A
// long-running tool outside the browser and filesystem groups is locked on
// its own, so it waits only for another call of itself.
func TestToolLockWaitEndsWithItsContext(t *testing.T) {
	locks := newToolLocks(longRunningPolicy{})
	release, err := locks.acquire(t.Context(), "exec")
	if err != nil {
		t.Fatalf("acquire exec: %v", err)
	}
	defer release()
	other, err := locks.acquire(t.Context(), "webmcp_open_tab")
	if err != nil {
		t.Fatalf("acquire a browser tool beside exec: %v", err)
	}
	other()
	cause := errors.New("delegation cancelled")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := locks.acquire(ctx, "exec"); !errors.Is(err, cause) {
		t.Fatalf("second exec acquire = %v, want the cancel cause", err)
	}
}
