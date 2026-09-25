// Package browserstream owns the bounded delivery policy for host browser
// events entering a room. Hosts supply only a per-event field projection.
package browserstream

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// Capacity bounds the events buffered for one participant's browser watch.
const Capacity = 16

// Forward projects host events onto a bounded room event channel. Delivery
// never blocks the host producer: an event that finds the queue full is
// dropped, and the output closes when the input closes or ctx ends.
func Forward[T any](ctx context.Context, input <-chan T, convert func(T) rooms.BrowserEvent) <-chan rooms.BrowserEvent {
	if ctx == nil || input == nil || convert == nil {
		return nil
	}
	output := make(chan rooms.BrowserEvent, Capacity)
	go forward(ctx, input, convert, output)
	return output
}

func forward[T any](ctx context.Context, input <-chan T, convert func(T) rooms.BrowserEvent, output chan<- rooms.BrowserEvent) {
	defer close(output)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-input:
			if !ok {
				return
			}
			if !offer(ctx, output, convert(event)) {
				return
			}
		}
	}
}

func offer(ctx context.Context, output chan<- rooms.BrowserEvent, event rooms.BrowserEvent) bool {
	select {
	case output <- event:
		return true
	case <-ctx.Done():
		return false
	default:
		return true
	}
}
