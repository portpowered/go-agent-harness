package subsystems

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// DefaultContextPressureWarningMessage is the warning injected when context pressure
// exceeds the configured threshold.
const DefaultContextPressureWarningMessage = "You are approaching the context limit. Save your progress and finish your current task. You will be re-instantiated with fresh context."

// InterruptWriter can write a user message to the agent loop's control plane.
// Implemented by *participants.UserRunner.
type InterruptWriter interface {
	Write(ctx context.Context, msg messages.Message) error
}

// ContextPressureNotifier monitors token usage and injects a warning interrupt once
// when the conversation buffer exceeds a configurable fraction of the max token limit.
//
// It fires exactly once per agent session. Once fired, subsequent ticks are no-ops.
//
// The warning is delivered via the interrupt mechanism: a ControlPlanePart with type
// interrupt is written to the user runner so the InterruptHandler picks it up on the
// next tick, cancels in-flight executions, appends the warning as a user turn, and
// dispatches a fresh InferenceRequest.
//
// ContextPressureNotifier runs at TickGroupContextPressureNotifier (45), after
// TokenCounter (40) so it has accurate token counts for the current tick.
type ContextPressureNotifier struct {
	counter        TokenCounter
	maxTokens      int
	threshold      float64 // fraction [0.0, 1.0], e.g. 0.8
	warningMessage string
	writer         InterruptWriter
	fired          bool
}

// NewContextPressureNotifier returns a ContextPressureNotifier. Returns an error if
// counter is nil — the TokenCounter is required and there is no silent fallback.
func NewContextPressureNotifier(
	counter TokenCounter,
	maxTokens int,
	threshold float64,
	warningMessage string,
	writer InterruptWriter,
) (*ContextPressureNotifier, error) {
	if counter == nil {
		return nil, errors.New("context pressure notifier: TokenCounter is required")
	}
	if warningMessage == "" {
		warningMessage = DefaultContextPressureWarningMessage
	}
	return &ContextPressureNotifier{
		counter:        counter,
		maxTokens:      maxTokens,
		threshold:      threshold,
		warningMessage: warningMessage,
		writer:         writer,
	}, nil
}

var _ Subsystem = (*ContextPressureNotifier)(nil)

// TickGroup implements Subsystem. Returns TickGroupContextPressureNotifier (45),
// which is after TokenCounter (40) so token counts are up-to-date.
func (n *ContextPressureNotifier) TickGroup() TickGroup {
	return TickGroupContextPressureNotifier
}

// Execute implements Subsystem. Checks whether token usage has crossed the threshold
// and, if so, sends a single interrupt with the warning message.
func (n *ContextPressureNotifier) Execute(ctx context.Context, curr *state.LoopState) error {
	if n.fired {
		return nil
	}
	if n.maxTokens <= 0 || n.threshold <= 0 {
		return nil
	}

	thresholdTokens := int(float64(n.maxTokens) * n.threshold)
	count := n.counter.Count(curr.History.ConversationBuffer)
	if count < thresholdTokens {
		return nil
	}

	// Build the interrupt message. The ControlPlanePart signals an interrupt;
	// the TextPart carries the warning so the InterruptHandler appends it as
	// a user turn before dispatching the next inference.
	interruptMsg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt},
			messages.NewTextPart(n.warningMessage),
		},
	}

	if n.writer != nil {
		if err := n.writer.Write(ctx, interruptMsg); err != nil {
			return fmt.Errorf("context pressure notifier: failed to send interrupt: %w", err)
		}
	}

	n.fired = true
	return nil
}
