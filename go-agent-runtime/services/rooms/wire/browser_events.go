package wire

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/browserstream"
)

// BrowserEventStream adapts a host browser event channel to the bounded room
// browser watch. The host supplies only the field projection; buffering,
// drop, and shutdown policy belong to the room service.
func BrowserEventStream[T any](ctx context.Context, input <-chan T, convert func(T) rooms.BrowserEvent) <-chan rooms.BrowserEvent {
	return browserstream.Forward(ctx, input, convert)
}
