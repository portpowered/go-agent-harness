package stream

import "context"

// ReleaseOnEvent turns a validated host event into one finite input release.
// The runtime owns cancellation, cloning and channel closure; event decoding
// remains in the host adapter.
func contextOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func releaseOnEvent[E, V any](parent context.Context, events <-chan E, inputs []V, match func(E) bool, clone func(V) V) (<-chan V, func()) {
	ctx, cancel := context.WithCancel(contextOrBackground(parent))
	out := make(chan V, len(inputs))
	go func() {
		defer close(out)
		defer cancel()
		_, ok := waitForEvent(ctx, events, match)
		if ok {
			sendEventInputs(ctx, out, inputs, clone)
		}
	}()
	return out, cancel
}

func waitForEvent[E any](ctx context.Context, events <-chan E, match func(E) bool) (E, bool) {
	for {
		select {
		case <-ctx.Done():
			var zero E
			return zero, false
		case event, ok := <-events:
			if !ok {
				var zero E
				return zero, false
			}
			if match != nil && match(event) {
				return event, true
			}
		}
	}
}

func sendEventInputs[V any](ctx context.Context, out chan<- V, inputs []V, clone func(V) V) {
	for _, input := range inputs {
		if clone != nil {
			input = clone(input)
		}
		select {
		case out <- input:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) ReleaseOnInvocation(parent context.Context, events <-chan InvocationEvent, inputs []ScheduledInput, toolName string) (<-chan ScheduledInput, func()) {
	return releaseOnEvent(parent, events, inputs, func(event InvocationEvent) bool {
		return event.Dispatched && event.InvocationID != "" && event.ToolName != "" && (toolName == "" || event.ToolName == toolName)
	}, CloneScheduled)
}
