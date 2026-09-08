package engine

import (
	"context"
	"sort"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// SetClock injects the time domain for hot-loop pacing before the loop starts.
// Nil retains the constructor's canonical real clock for existing callers.
func (e *Engine) SetClock(source clock.TimerSource) {
	if source != nil {
		e.clock = source
	}
}

func (e *Engine) waitForNextTick(ctx context.Context, started time.Time) error {
	if e.tickRate <= 0 {
		return nil
	}
	remaining := e.tickRate - e.clock.Now().Sub(started)
	if remaining <= 0 {
		return nil
	}
	return clock.Wait(ctx, e.clock, remaining)
}

// orderedSubsystems sorts helpers by their tick group for deterministic execution.
func orderedSubsystems(hlps []subsystems.Subsystem) []subsystems.Subsystem {
	sorted := make([]subsystems.Subsystem, len(hlps))
	copy(sorted, hlps)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TickGroup() < sorted[j].TickGroup()
	})
	return sorted
}
