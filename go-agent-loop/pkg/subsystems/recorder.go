package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// StateRecorder is called at the end of recording boundaries to persist state.
type StateRecorder interface {
	Record(ctx context.Context, messages []messages.Message) error
}

// Recorder saves conversation state at configurable boundaries.
type Recorder struct {
	recorder StateRecorder
	// tickInterval controls how often recording happens (every N ticks). 0 means every tick.
	tickInterval int
	tickCount    int
}

var _ Subsystem = (*Recorder)(nil)

func NewRecorder(recorder StateRecorder, tickInterval int) *Recorder {
	return &Recorder{
		recorder:     recorder,
		tickInterval: tickInterval,
	}
}

func (h *Recorder) TickGroup() TickGroup {
	return TickGroupRecorder
}

// Flush records the conversation now, regardless of the tick interval. The
// engine calls it when the loop exits outside a tick after changing history.
func (h *Recorder) Flush(ctx context.Context, curr *state.LoopState) error {
	if h.recorder == nil {
		return nil
	}
	return h.recorder.Record(ctx, curr.History.ConversationBuffer)
}

func (h *Recorder) Execute(ctx context.Context, curr *state.LoopState) error {
	if h.recorder == nil {
		return nil
	}

	h.tickCount++
	if h.tickInterval > 0 && h.tickCount%h.tickInterval != 0 {
		return nil
	}

	return h.recorder.Record(ctx, curr.History.ConversationBuffer)
}
