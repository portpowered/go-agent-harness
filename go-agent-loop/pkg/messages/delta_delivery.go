package messages

import "context"

// MustDeliver reports whether losing msg would corrupt the turn: a tool call
// or tool result, a message or session boundary, an error, LOOP.END, or an
// assembled SYSTEM.FULL_MESSAGE. Such deltas wait for buffer capacity
// instead of being dropped when a consumer falls behind.
func MustDeliver(msg StreamMessage) bool {
	if msg.Role == RoleTool {
		return true
	}
	switch msg.Type { //nolint:exhaustive // Only lifecycle and tool-call deltas must be delivered.
	case StreamTypeToolCallStart,
		StreamTypeToolCallDelta,
		StreamTypeToolCallEnd,
		StreamTypeMessageStart,
		StreamTypeMessageEnd,
		StreamTypeError,
		StreamTypeSessionClose,
		StreamTypeLoopEnd,
		StreamTypeSystemFullMessage:
		return true
	default:
		return false
	}
}

// WriteStreamDelta writes msg to buf. A delta that [MustDeliver] waits for
// capacity until ctx ends; any other delta keeps the non-blocking overload
// policy of [TypedBuffer.WriteContext] and is counted in Drops when buf is
// full.
func WriteStreamDelta(ctx context.Context, buf *TypedBuffer[StreamMessage], msg StreamMessage) BufferWriteOutcome {
	if MustDeliver(msg) {
		return buf.WriteWaitContext(ctx, msg)
	}
	return buf.WriteContext(ctx, msg)
}

// WriteKernelDelta writes req to the kernel's delta inbox with the same
// policy as [WriteStreamDelta].
func WriteKernelDelta(ctx context.Context, buf *TypedBuffer[KernelDeltaRequest], req KernelDeltaRequest) BufferWriteOutcome {
	if MustDeliver(req.Delta) {
		return buf.WriteWaitContext(ctx, req)
	}
	return buf.WriteContext(ctx, req)
}
