package participants

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// InteractionRunner is an input-only participant used to feed normalized
// gateway interaction events into the agent loop.
type InteractionRunner struct {
	Outbox     *messages.TypedBuffer[messages.InteractionEvent]
	actorIndex int
}

func NewInteractionRunner(bufferCapacity int) *InteractionRunner {
	return &InteractionRunner{
		Outbox: messages.NewTypedBuffer[messages.InteractionEvent](bufferCapacity),
	}
}

func (r *InteractionRunner) Write(ctx context.Context, event messages.InteractionEvent) error {
	if ok := r.Outbox.Write(ctx, event); !ok {
		return fmt.Errorf("failed to write interaction event, buffer is full")
	}
	r.actorIndex++
	return nil
}

func (r *InteractionRunner) WriteBatch(ctx context.Context, events []messages.InteractionEvent) error {
	for _, event := range events {
		if err := r.Write(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
